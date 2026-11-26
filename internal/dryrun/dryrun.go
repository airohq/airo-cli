// Package dryrun gives a command that creates or changes resources kubectl's
// --dry-run and -o: a client that sends each write with the API's dry run,
// or not at all, records the resource as it would be, and prints what it
// recorded (DESIGN.md#arguments-and-flags).
package dryrun

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// Options are a command's --dry-run and -o.
type Options struct {
	cmd      *cobra.Command
	strategy cmdutil.DryRunStrategy
	output   string
}

// AddFlags adds --dry-run and -o to cmd, and checks them before it runs.
func AddFlags(cmd *cobra.Command) *Options {
	o := &Options{cmd: cmd}
	cmdutil.AddDryRunFlag(cmd)
	cmd.Flags().Lookup("dry-run").Usage = "show what the command would create or change, and keep nothing: server has the cluster check it, client sends nothing"
	cmd.Flags().StringVarP(&o.output, "output", "o", "", "with --dry-run, print the resources as yaml or json")
	_ = cmd.RegisterFlagCompletionFunc("output", cobra.FixedCompletions([]string{"yaml", "json"}, cobra.ShellCompDirectiveNoFileComp))
	next := cmd.PreRunE
	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		if err := o.parse(); err != nil {
			return err
		}
		if next != nil {
			return next(cmd, args)
		}
		return nil
	}
	return o
}

// parse reads the flags as kubectl does: a bare --dry-run means client.
func (o *Options) parse() error {
	s, err := cmdutil.GetDryRunStrategy(o.cmd)
	if err != nil {
		return err
	}
	switch o.output {
	case "", "yaml", "json":
	default:
		return fmt.Errorf("-o %s is not supported: use yaml or json", o.output)
	}
	if o.output != "" && s == cmdutil.DryRunNone {
		return fmt.Errorf("-o %s shows what a dry run would do: add --dry-run=server, or --dry-run=client to send nothing", o.output)
	}
	o.strategy = s
	return nil
}

// Active reports whether the command is to write nothing that persists. A
// nil Options, of a command without the flags, is never active.
func (o *Options) Active() bool { return o != nil && o.strategy != cmdutil.DryRunNone }

// Client returns c for the command's writes: c itself, or for a dry run a
// client that records them.
func (o *Options) Client(c client.Client) client.Client {
	if !o.Active() {
		return c
	}
	return &recorder{Client: c, strategy: o.strategy, output: o.output, out: o.cmd.OutOrStdout(), errOut: o.cmd.ErrOrStderr()}
}

// Note adds a line that Finish prints on standard error, where the
// resources differ from what the real run would send, when c is a dry
// run's client.
func Note(c client.Client, text string) {
	if r, ok := c.(*recorder); ok {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.notes = append(r.notes, text)
	}
}

// Requested reports whether cmd's --dry-run asks for a dry run, before the
// command checks its flags.
func Requested(cmd *cobra.Command) bool {
	f := cmd.Flags().Lookup("dry-run")
	return f != nil && f.Value.String() != "none" && f.Value.String() != "false"
}

// Is reports whether c records its writes instead of persisting them.
func Is(c client.Client) bool {
	_, ok := c.(*recorder)
	return ok
}

// Finish prints what a dry run's client recorded, and reports whether c
// was one. A command calls it after its writes, and returns when it was:
// what would come next, such as waiting until a resource is ready, cannot
// happen for a resource that does not persist.
func Finish(c client.Client) (bool, error) {
	r, ok := c.(*recorder)
	if !ok {
		return false, nil
	}
	return true, r.print()
}

// write is a resource as a write would leave it.
type write struct {
	object client.Object
	verb   string
}

// recorder is a client whose writes persist nothing. It reads as c does.
type recorder struct {
	client.Client
	strategy cmdutil.DryRunStrategy
	output   string
	out      io.Writer
	errOut   io.Writer

	mu     sync.Mutex
	writes []write
	notes  []string
}

func (r *recorder) record(obj client.Object, verb string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes = append(r.writes, write{obj.DeepCopyObject().(client.Object), verb})
}

func (r *recorder) server() bool { return r.strategy == cmdutil.DryRunServer }

func (r *recorder) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if r.server() {
		built := obj.DeepCopyObject().(client.Object)
		err := r.Client.Create(ctx, obj, append(opts, client.DryRunAll)...)
		if apierrors.IsNotFound(err) && r.wrote() {
			// It refers to a resource this dry run made, which does not
			// exist, as a RoleBinding refers to its Role. The real run
			// makes that one first.
			kind, _ := r.kind(built)
			Note(r, fmt.Sprintf("the cluster could not check %s/%s, which refers to a resource of this dry run: %v", kind, built.GetName(), err))
			r.record(built, "created")
			return nil
		}
		if err != nil {
			return err
		}
	}
	r.record(obj, "created")
	return nil
}

func (r *recorder) wrote() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.writes) > 0
}

func (r *recorder) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if r.server() {
		if err := r.Client.Update(ctx, obj, append(opts, client.DryRunAll)...); err != nil {
			return err
		}
	}
	r.record(obj, "configured")
	return nil
}

// Patch shows the patched resource only with the server's dry run: the
// client's would need the patch applied here.
func (r *recorder) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if r.server() {
		if err := r.Client.Patch(ctx, obj, patch, append(opts, client.DryRunAll)...); err != nil {
			return err
		}
	}
	r.record(obj, "patched")
	return nil
}

// Delete deletes nothing: what a dry run made does not exist.
func (r *recorder) Delete(context.Context, client.Object, ...client.DeleteOption) error {
	return nil
}

// print writes a line per write, as kubectl does, or with -o the resources.
func (r *recorder) print() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, note := range r.notes {
		fmt.Fprintln(r.errOut, "Note: "+note)
	}
	suffix := "(dry run)"
	if r.server() {
		suffix = "(server dry run)"
	}
	if r.output == "" {
		if len(r.writes) == 0 {
			_, err := fmt.Fprintf(r.out, "Nothing to create or change %s\n", suffix)
			return err
		}
		for _, w := range r.writes {
			kind, err := r.kind(w.object)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(r.out, "%s/%s %s %s\n", kind, w.object.GetName(), w.verb, suffix); err != nil {
				return err
			}
		}
		return nil
	}
	var items []any
	for _, w := range r.writes {
		obj, err := r.plain(w.object)
		if err != nil {
			return err
		}
		items = append(items, obj)
	}
	var doc any = map[string]any{"apiVersion": "v1", "kind": "List", "items": items}
	if len(items) == 1 {
		doc = items[0]
	}
	if items == nil {
		doc = map[string]any{"apiVersion": "v1", "kind": "List", "items": []any{}}
	}
	var data []byte
	var err error
	if r.output == "json" {
		data, err = json.MarshalIndent(doc, "", "    ")
		data = append(data, '\n')
	} else {
		data, err = yaml.Marshal(doc)
	}
	if err != nil {
		return err
	}
	_, err = r.out.Write(data)
	return err
}

// kind is how kubectl names a kind in its output, such as agent.api.kagent.dev.
func (r *recorder) kind(obj client.Object) (string, error) {
	gvk, err := r.GroupVersionKindFor(obj)
	if err != nil {
		return "", err
	}
	kind := strings.ToLower(gvk.Kind)
	if gvk.Group != "" {
		kind += "." + gvk.Group
	}
	return kind, nil
}

// plain is the resource as -o prints it: with its apiVersion and kind, which
// a typed object read from the API lacks, and without managedFields, which
// kubectl's -o yaml hides too.
func (r *recorder) plain(obj client.Object) (map[string]any, error) {
	gvk, err := r.GroupVersionKindFor(obj)
	if err != nil {
		return nil, err
	}
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}
	m["apiVersion"], m["kind"] = gvk.GroupVersion().String(), gvk.Kind
	if meta, ok := m["metadata"].(map[string]any); ok {
		delete(meta, "managedFields")
		if ts, ok := meta["creationTimestamp"]; ok && ts == nil {
			delete(meta, "creationTimestamp")
		}
	}
	delete(m, "status")
	return m, nil
}
