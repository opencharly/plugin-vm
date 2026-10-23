package vm

import (
	"context"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/loaderkit"
)

// vm_backend_resolve.go — the VM backend detection capability. The detection
// itself is now the VM-backend PROVIDER CLASS (vm_backend_class.go): every
// provider owns its own Available() probe, and `auto` walks the schema order
// (libvirt, then qemu) over the auto-detectable providers. This file keeps the
// two thin, named entry points the config-resolve path calls, so the class has
// ONE resolver and callers do not re-derive it.
//
// The entity's `backend:` pin is self-loaded PLUGIN-SIDE
// (loaderkit.ResolveVmEntityViaExecutor); the plugin is COMPILED-IN, so the
// process cwd is the host's cwd and no deploy-plugins-connect indirection is
// needed.

// resolveVmBackendPlugin resolves the configured selector to its provider WORD.
// `auto` detects through the class; an explicit word must be Available.
func resolveVmBackendPlugin(configured string) (string, error) {
	p, err := resolveVmBackendProvider(configured)
	if err != nil {
		return "", err
	}
	return string(p.Word()), nil
}

// vmConfiguredBackendPlugin returns the backend selector to feed
// resolveVmBackendPlugin for a vm entity: the entity's `backend:` pin when set,
// else the global vm.backend setting (rtBackend). THE single source so EVERY vm
// verb resolves the SAME backend for a given entity.
func vmConfiguredBackendPlugin(ctx context.Context, ex *sdk.Executor, vmName, rtBackend string) string {
	if vmName == "" {
		return rtBackend
	}
	vm, err := loaderkit.ResolveVmEntityViaExecutor(ctx, ex, "", vmName)
	if err != nil || vm == nil || vm.Backend == "" {
		return rtBackend
	}
	return vm.Backend
}
