package worker

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/znasllc-io/memql-cockpit/internal/worker/hardware"
	"github.com/znasllc-io/memql-cockpit/internal/worker/inference"
	"github.com/znasllc-io/memql-cockpit/internal/worker/models"
)

// `memql worker setup --runtime kokoro|image` -- the speech and image
// runtimes (memql-cockpit#400, engine record D7).
//
// Same shape and the same rules as `setup --inference`, because it is
// the same person at the same terminal: the commands are PRINTED BEFORE
// THE QUESTION and the question covers all of them at once; nothing is
// installed that nobody approved; --non-interactive refuses with exit 3
// rather than assuming a yes; and a second run installs nothing and says
// so in the present tense.
//
// NO BACKGROUND SERVICE IS STARTED UNASKED. The install line starts a
// container, which IS starting a service -- so it is in the list that is
// printed and consented to, never behind it.

type runtimeSetup struct {
	out io.Writer
	in  io.Reader

	name           string
	nonInteractive bool

	gather  func(ctx context.Context) (inference.RuntimeHost, error)
	install func(ctx context.Context, cmds []string) error
	probe   func(ctx context.Context) (bool, string)
}

func newRuntimeSetup(configPath, name string, nonInteractive bool) *runtimeSetup {
	policyPath := filepath.Join(filepath.Dir(configPath), "policy.yaml")
	discoverer := &models.Discoverer{}
	return &runtimeSetup{
		out:            os.Stdout,
		in:             os.Stdin,
		name:           name,
		nonInteractive: nonInteractive,
		gather: func(ctx context.Context) (inference.RuntimeHost, error) {
			return gatherRuntimeHost(ctx, discoverer, policyPath)
		},
		install: func(ctx context.Context, cmds []string) error {
			return inference.RunCommands(ctx, cmds, inference.ExecRunner(os.Stdout))
		},
		probe: func(ctx context.Context) (bool, string) { return probeRuntime(ctx, name) },
	}
}

// gatherRuntimeHost assembles the facts DecideRuntime reads.
//
// It reuses the hardware package's runtime scan rather than probing
// again, for the reason every duplicated probe in this repository gets
// deleted: two readers of "is Kokoro running" drift, and the failure is
// a command that installs a second container beside the one that is
// already answering.
func gatherRuntimeHost(ctx context.Context, d *models.Discoverer, policyPath string) (inference.RuntimeHost, error) {
	host, err := inference.Gather(ctx, d)
	if err != nil {
		return inference.RuntimeHost{}, err
	}

	out := inference.RuntimeHost{
		GOOS:          host.GOOS,
		GOARCH:        host.GOARCH,
		Docker:        host.Docker,
		OllamaPresent: host.RuntimePresentFor(),
		OllamaBase:    d.ResolvedOllamaBaseURL(),
	}

	for _, rt := range host.Hardware.Runtimes {
		if rt.Name == hardware.RuntimeKokoro {
			out.KokoroPresent = true
			out.KokoroDetail = kokoroDetail(rt.Version)
		}
	}

	// The image-capable model comes from the RUNTIME's own capability
	// list, through the same probe that decides the `imagegen` label.
	// A second source here could advertise a modality the serving path
	// does not agree with.
	inv := probeWithPolicy(ctx, d, policyPath)
	for _, m := range inv.Models {
		if m.ImageGen {
			out.ImageCapableModel = m.ID
			break
		}
	}
	return out, nil
}

func kokoroDetail(version string) string {
	base := hardware.KokoroBaseURL()
	if strings.TrimSpace(version) == "" {
		return base
	}
	return fmt.Sprintf("%s (%s)", base, version)
}

// probeRuntime re-reads whether the runtime answers, AFTER an install.
//
// THE LABEL APPEARS ONLY ONCE THE RUNTIME ANSWERS A PROBE, which is
// issue #400's acceptance criterion -- so the closing block reports what
// a probe found, never that a command exited zero. A container that
// started and then died on its first request would otherwise be reported
// as a runtime this machine has.
func probeRuntime(ctx context.Context, name string) (bool, string) {
	for _, rt := range hardware.Local(ctx).Runtimes {
		switch {
		case name == inference.RuntimeKokoro && rt.Name == hardware.RuntimeKokoro:
			return true, kokoroDetail(rt.Version)
		}
	}
	return false, ""
}

func runRuntimeSetup(ctx context.Context, s *runtimeSetup) int {
	host, err := s.gather(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "this machine could not be inspected: %v\n", err)
		return SetupExitOpFailed
	}
	plan := inference.DecideRuntime(host, s.name)

	s.line(fmt.Sprintf("Setting this machine up to serve %s.", runtimeServes(plan.Name)))
	s.line("")

	// IDEMPOTENCE, said in the present tense. A re-run that printed the
	// install commands again reads as a machine that had lost the
	// runtime, and somebody would run them -- producing a container name
	// conflict against the one that is already working.
	if plan.Present {
		s.paragraph(fmt.Sprintf("%s is already running%s. Nothing to install.",
			runtimeTitle(plan.Name), detailSuffix(plan.Detail)))
		s.line("")
		s.printAdvertisement(ctx, plan.Name)
		return SetupExitOK
	}

	if plan.Refusal != "" {
		s.paragraph(plan.Refusal)
		return SetupExitPrereq
	}

	s.line(fmt.Sprintf("%s is not installed. This machine needs it to serve %s.",
		runtimeTitle(plan.Name), runtimeServes(plan.Name)))
	if note := strings.TrimSpace(plan.Note); note != "" {
		s.line("")
		s.paragraph(note)
	}
	s.line("")
	s.line("These commands will run:")
	for _, cmd := range plan.Install {
		s.line("  " + cmd)
	}

	if !s.ask() {
		s.line("")
		if s.nonInteractive {
			s.line("Nothing was installed: --non-interactive cannot answer that question.")
			s.line("Run this again without it, or run the commands above yourself.")
		} else {
			s.line("Nothing was installed.")
		}
		return SetupExitRefused
	}

	if err := s.install(ctx, plan.Install); err != nil {
		s.line("")
		s.paragraph(capitalise(err.Error()) + ".")
		return SetupExitOpFailed
	}
	s.line("")
	s.printAdvertisement(ctx, plan.Name)
	return SetupExitOK
}

// printAdvertisement says what the cluster will see, and refuses to
// overstate it.
//
// It PROBES rather than trusting the install: a container that started
// and then died is a command that exited zero and a runtime that is not
// there. And a runtime that answers is still not visible to the cluster
// until the next reconnect -- labels bind at Register -- so the sentence
// says "within a minute or two" rather than "available now", the same
// distinction printReadvertise draws.
func (s *runtimeSetup) printAdvertisement(ctx context.Context, name string) {
	present, detail := s.probe(ctx)
	if !present {
		s.paragraph(fmt.Sprintf(
			"%s is not answering yet, so this machine advertises nothing for it. Give it a moment"+
				" and run this again; the label appears only once the runtime answers a probe.",
			runtimeTitle(name)))
		return
	}
	s.paragraph(fmt.Sprintf("%s is answering at %s. The cluster sees runtime:%s once this worker"+
		" reconnects, which takes a minute or two.", runtimeTitle(name), detail, name))
}

func (s *runtimeSetup) ask() bool {
	if s.nonInteractive {
		return false
	}
	fmt.Fprint(s.out, "\nRun them now? [y/N] ")
	line, err := bufio.NewReader(s.in).ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		fmt.Fprintln(s.out, "")
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// runtimeName is what the runtime is CALLED, and runtimeWord is what it
// DOES. Both are needed in the same sentence often enough that
// collapsing them produces "Kokoro is not installed, this machine needs
// it to serve kokoro".
func runtimeTitle(name string) string {
	switch name {
	case inference.RuntimeKokoro:
		return "Kokoro"
	case inference.RuntimeImage:
		return "Image generation"
	default:
		return name
	}
}

func runtimeServes(name string) string {
	switch name {
	case inference.RuntimeKokoro:
		return "speech"
	case inference.RuntimeImage:
		return "image generation"
	default:
		return name
	}
}

func detailSuffix(detail string) string {
	if strings.TrimSpace(detail) == "" {
		return ""
	}
	return " at " + detail
}

func (s *runtimeSetup) line(text string) { fmt.Fprintln(s.out, text) }

func (s *runtimeSetup) paragraph(text string) {
	for _, l := range wrapText(text, inferenceWrapWidth) {
		fmt.Fprintln(s.out, l)
	}
}
