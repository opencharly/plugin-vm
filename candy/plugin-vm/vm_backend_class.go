package vm

import (
	"fmt"

	"github.com/opencharly/spec/spec"
)

// vm_backend_class.go — the VM-backend PROVIDER CLASS (W3.4).
//
// Before this file a VM backend was a hardcoded word checked at six `switch
// backend` sites (`vm_backend_resolve.go`, `vm.go`, `provider.go`). Adding
// `krun` would have made that seven sites and a third literal. Instead every
// backend is a provider in ONE registry keyed by the SCHEMA-owned vocabulary
// (`spec.VmBackends`, generated from `#VmBackendName`), and every dispatch site
// resolves the provider and calls it. The words below are the registry KEYS —
// the same shape `spec/container.engineCapabilities` uses for engine words —
// never a dispatch branch.
//
// The class is resolved by WORD here (the plugin model's `vmbackend:<word>`
// identity). It is deliberately an in-package class rather than a
// `plugin.providers` capability: `charly/provider.go` has no `ClassVMBackend`,
// and `spec`/`sdk` carry no `vmbackend` op envelope, so an out-of-process
// `plugin-vm-krun` cannot participate in dispatch yet. The krun provider is a
// separate FILE SET so promoting it to its own repo (and its own class
// registration) is a move, not a rewrite.

// The VM-backend provider words. These are registry keys, not dispatch
// branches; vmBackendRegistryDrift pins them against spec.VmBackends.
const (
	vmBackendLibvirt = spec.VmBackend("libvirt")
	vmBackendQemu    = spec.VmBackend("qemu")
	vmBackendKrun    = spec.VmBackend("krun")

	// vmBackendAuto is the RESOLUTION selector, never a provider word
	// (#VmBackend adds it above #VmBackendName).
	vmBackendAuto = "auto"
)

// vmBackendProvider is one member of the VM-backend class. A method returns a
// found=false / torn=false result rather than an error when the provider simply
// does not hold a VM, so the class can probe every member without turning
// "absent" into "failed".
type vmBackendProvider interface {
	// Word is the provider's schema word (`spec.VmBackends` member).
	Word() spec.VmBackend
	// AutoDetectable reports whether `backend: auto` may select this provider.
	// An explicit-only backend (krun) is never auto-detected.
	AutoDetectable() bool
	// SupportsPCIHostdev reports whether a PCI <hostdev> (GPU passthrough)
	// renders on this backend. It replaces the "backend == libvirt" literal the
	// GPU auto-allocation path used to carry.
	SupportsPCIHostdev() bool
	// Available probes the provider's host prerequisites. A non-nil error is
	// operator-facing and names what to install/configure.
	Available() error
	// Holder reports whether this provider holds the VM `name`.
	Holder(name string) (found, running bool, stateDir string)
	// Start starts an existing VM. alreadyRunning is the idempotent no-op signal.
	Start(name string) (alreadyRunning bool, err error)
	// Stop stops it. force asks for an immediate stop where the provider has one;
	// a provider whose guest must power off gracefully may ignore it (krun never
	// signals a systemd guest).
	Stop(name string, force bool) error
	// Destroy removes the VM. torn is false only when it was not this provider's.
	Destroy(name, stateDir string, deleteDisk bool) (torn bool, err error)
	// Create creates the VM from the host-resolved request.
	Create(req vmCreateReq) error
	// RenderForValidation renders egress-validatable config ("" = nothing to
	// validate, e.g. a backend with no domain XML).
	RenderForValidation(req vmCreateReq) (string, error)
	// Console attaches to (or dumps) the guest console.
	Console(name string, dump bool, lines int) error
	// Screenshot captures the guest framebuffer to `out`.
	Screenshot(name, out string) error
}

// vmBackendProviders is the class registry. Adding a provider is one entry here
// plus its file; no dispatch site changes.
var vmBackendProviders = map[spec.VmBackend]vmBackendProvider{
	vmBackendLibvirt: libvirtBackend{},
	vmBackendQemu:    qemuBackend{},
	vmBackendKrun:    krunBackend{},
}

// vmBackendOrder is the SCHEMA order (libvirt, qemu, krun). Holder probing and
// auto-detection follow it, so the order is a data fact rather than a second
// list to keep in sync.
var vmBackendOrder = spec.VmBackends

// vmBackendProviderFor resolves an explicit backend word to its provider. An
// empty or unknown word is a hard error, never a silent default: the previous
// switch silently fell through to "nothing happened" for krun.
func vmBackendProviderFor(word string) (vmBackendProvider, error) {
	if word == "" {
		return nil, fmt.Errorf("no VM backend selected (want one of %v)", spec.VmBackends)
	}
	p, ok := vmBackendProviders[spec.VmBackend(word)]
	if !ok {
		return nil, fmt.Errorf("unknown VM backend %q (want one of %v)", word, spec.VmBackends)
	}
	return p, nil
}

// resolveVmBackendProvider is the ONE resolver behind `backend:` — explicit
// words resolve to their provider (and must be Available), `auto` walks the
// schema order over the auto-detectable providers. It NEVER resolves krun
// implicitly: krun is explicit-only, exactly as `deploykit.ResolveVmBackend`
// documents.
func resolveVmBackendProvider(configured string) (vmBackendProvider, error) {
	if configured == "" {
		configured = vmBackendAuto
	}
	if configured != vmBackendAuto {
		p, err := vmBackendProviderFor(configured)
		if err != nil {
			return nil, err
		}
		if err := p.Available(); err != nil {
			return nil, err
		}
		return p, nil
	}
	var probed []string
	for _, word := range vmBackendOrder {
		p := vmBackendProviders[spec.VmBackend(word)]
		if p == nil || !p.AutoDetectable() {
			continue
		}
		probed = append(probed, word)
		if p.Available() == nil {
			return p, nil
		}
	}
	return nil, fmt.Errorf("no VM backend available (probed %v; install libvirt or qemu-system, or select an explicit backend)", probed)
}

// vmBackendHolder is the ONE authoritative "which backend ACTUALLY holds this
// VM" probe (#69/#77): every provider is asked in schema order and the first
// claim wins. It never trusts the resolved/default backend, so a krun container
// is not mistaken for a qemu state dir and vice versa.
func vmBackendHolder(name string) (p vmBackendProvider, running bool, stateDir string, err error) {
	for _, word := range vmBackendOrder {
		provider := vmBackendProviders[spec.VmBackend(word)]
		if provider == nil {
			continue
		}
		if found, running, stateDir := provider.Holder(name); found {
			return provider, running, stateDir, nil
		}
	}
	return nil, false, "", nil
}
