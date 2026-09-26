package vm

import (
	"fmt"
	"os"

	"github.com/opencharly/spec/container"
)

// vm_retag.go — `charly vm retag <src> <dst>`: give a locally-built VM box a
// STABLE, addressable ref.
//
// `charly vm build` tags its emitted box with the wall-clock CalVer
// (`localhost/charly-<vm>:<YYYY.DDD.HHMM>`), so a consumer that must NAME the
// image statically — a `kind:kubevirt` `container_disk.image`, or any authored
// ref — cannot know the tag in advance. This verb retags an existing local image
// (resolved by short name or full ref, exactly like `charly vm cp-box`) to a
// deterministic ref the author controls. With `--push` it also pushes the
// destination so a cluster can pull it by that ref.
type VmRetagCmd struct {
	Src    string `arg:"" help:"Source image ref present in host engine storage (short name or full ref)"`
	Dst    string `arg:"" help:"Destination ref to tag it as (e.g. localhost/charly-myvm:stable)"`
	Push   bool   `name:"push" help:"Also push the destination ref to its registry after retagging"`
	Engine string `name:"engine" help:"Container engine (default: the detected engine)"`
}

func (c *VmRetagCmd) Run() error {
	engine := c.Engine
	if engine == "" {
		detected, err := container.DetectEngine()
		if err != nil {
			return err
		}
		engine = detected
	}
	// Resolve the source the SAME way cp-box does (the shared ResolveDeliverableRef):
	// a short name elects the newest matching local image; a full ref passes through.
	ref, err := container.ResolveDeliverableRef(engine, c.Src)
	if err != nil {
		return fmt.Errorf("vm retag: %w", err)
	}
	if err := retagImage(engine, ref, c.Dst); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Retagged %s as %s\n", ref, c.Dst)
	if c.Push {
		// pushVmBox with src==dst skips the retag (already done) and pushes.
		if err := pushVmBox(engine, c.Dst, c.Dst); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Pushed %s\n", c.Dst)
	}
	return nil
}

// retagImage tags srcRef as dstRef with the engine. A same-ref retag is a no-op.
func retagImage(engine, srcRef, dstRef string) error {
	if dstRef == "" {
		return fmt.Errorf("retagImage: empty destination ref")
	}
	if srcRef == dstRef {
		return nil
	}
	return engineCmd(container.EngineBinary(engine), "tag", srcRef, dstRef)
}
