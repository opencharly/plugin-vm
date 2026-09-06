package vm

import "testing"

// TestApplyFromSnapshotOverride is the regression guard for the from: name:tag
// functional half (Cutover A addendum): a --from-snapshot <tag> build treats the
// entity as a CLONE of its own golden at the named snapshot (from_vm = the entity
// itself). No-op when fromSnapshot is empty.
func TestApplyFromSnapshotOverride(t *testing.T) {
	cases := []struct {
		name         string
		fromSnapshot string
		wantKind     string
		wantFromVm   string
		wantFromSnap string
	}{
		{
			name:         "from-snapshot set overrides to a clone of the entity itself",
			fromSnapshot: "golden",
			wantKind:     "clone",
			wantFromVm:   "omarchy-vm",
			wantFromSnap: "golden",
		},
		{
			name:         "empty from-snapshot is a no-op",
			fromSnapshot: "",
			wantKind:     "iso",
			wantFromVm:   "",
			wantFromSnap: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vmSpec := &VmSpec{Source: VmSource{Kind: "iso"}}
			sourceKind := "iso"
			applyFromSnapshotOverride(vmSpec, &sourceKind, "omarchy-vm", tc.fromSnapshot)
			if sourceKind != tc.wantKind {
				t.Errorf("sourceKind = %q, want %q", sourceKind, tc.wantKind)
			}
			if vmSpec.Source.Kind != tc.wantKind {
				t.Errorf("vmSpec.Source.Kind = %q, want %q", vmSpec.Source.Kind, tc.wantKind)
			}
			if vmSpec.Source.FromVm != tc.wantFromVm {
				t.Errorf("vmSpec.Source.FromVm = %q, want %q", vmSpec.Source.FromVm, tc.wantFromVm)
			}
			if vmSpec.Source.FromSnapshot != tc.wantFromSnap {
				t.Errorf("vmSpec.Source.FromSnapshot = %q, want %q", vmSpec.Source.FromSnapshot, tc.wantFromSnap)
			}
		})
	}
}
