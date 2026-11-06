package removal

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// flip changes the scalar leaf v, or reports that its kind cannot be flipped
// (a slice, map or pointer: aliasable, so a seal copy could not hold it).
func flip(v reflect.Value) error {
	switch v.Kind() {
	case reflect.String:
		v.SetString(v.String() + "z")
	case reflect.Bool:
		v.SetBool(!v.Bool())
	case reflect.Int, reflect.Int64:
		v.SetInt(v.Int() + 7)
	default:
		return fmt.Errorf("kind %s cannot be flipped (aliasable?)", v.Kind())
	}
	return nil
}

// leaves calls fn with the index path of every non-struct leaf under the
// field at path of type ft.
func leaves(path []int, ft reflect.Type, fn func(path []int)) {
	if ft.Kind() == reflect.Struct {
		for j := 0; j < ft.NumField(); j++ {
			leaves(append(append([]int{}, path...), j), ft.Field(j).Type, fn)
		}
		return
	}
	fn(path)
}

// TestSealCoversEveryExportedItemField (reviewer's guard) flips every
// exported leaf of a sealed Item, one at a time: each must break the seal.
// A leaf it cannot flip (a slice, map or pointer added later) fails the
// test, so a future exported field cannot slip outside the seal.
func TestSealCoversEveryExportedItemField(t *testing.T) {
	base := seal(Item{Kind: cleanup.KindWorktree, Path: "/x", Ref: "refs/heads/a", Name: "a", Saw: cleanup.StatusProven,
		SawTip: strings.Repeat("a", 40), SawLosses: cleanup.Losses{Unpushed: 1}, Approved: Needs{Confirm: true}})
	if !base.confirmed() {
		t.Fatal("base not confirmed")
	}
	ty := reflect.TypeOf(base)
	for i := 0; i < ty.NumField(); i++ {
		fd := ty.Field(i)
		if !fd.IsExported() {
			continue
		}
		leaves([]int{i}, fd.Type, func(path []int) {
			it := base
			if err := flip(reflect.ValueOf(&it).Elem().FieldByIndex(path)); err != nil {
				t.Errorf("Item.%s %v: %v", fd.Name, path, err)
				return
			}
			if it.confirmed() {
				t.Errorf("Item.%s %v changed but the seal still matches", fd.Name, path)
			}
		})
	}
}

// TestPreviewBasisCoversWhatConfirmTrusts flips, one at a time, every
// exported leaf of an ItemPreview that the constructors read: the input
// item's identity, the Fresh fields the shown Item derives from (Head on a
// detached preview, Tip on a branch one), every leaf of Needs and of
// Fresh.Losses, CanDeleteBranch and Approval. Each must make Confirmed
// refuse the preview as edited; a leaf it cannot flip fails the test.
func TestPreviewBasisCoversWhatConfirmTrusts(t *testing.T) {
	onBranch := ItemPreview{
		Item:     Item{Kind: cleanup.KindWorktree, Path: "/x", Ref: "refs/heads/a", Name: "a"},
		Fresh:    cleanup.Row{Kind: cleanup.KindWorktree, Name: "a", Path: "/x", Ref: "refs/heads/a", Status: cleanup.StatusLessSure, Tip: strings.Repeat("a", 40), Losses: cleanup.Losses{Unpushed: 1}},
		Approval: ApprovalNeedsConfirm, Needs: Needs{Confirm: true}, CanDeleteBranch: true,
	}
	detached := onBranch
	detached.Item.Ref, detached.Fresh.Ref, detached.Fresh.Tip = "", "", ""
	detached.Fresh.Head = strings.Repeat("b", 40)
	trusted := map[*ItemPreview][]string{
		&onBranch: {"Item.Kind", "Item.Path", "Item.Ref", "Item.Name", "Fresh.Kind", "Fresh.Name", "Fresh.Path", "Fresh.Ref",
			"Fresh.Status", "Fresh.Tip", "Fresh.Losses", "Needs", "CanDeleteBranch", "Approval"},
		&detached: {"Fresh.Head"},
	}
	for base, fields := range trusted {
		base.basis = basisOf(*base)
		if err := base.genuine(); err != nil {
			t.Fatal(err)
		}
		for _, name := range fields {
			sf, ok := reflect.TypeOf(*base).FieldByName(strings.Split(name, ".")[0])
			if !ok {
				t.Fatalf("no field %s", name)
			}
			path, ft := sf.Index, sf.Type
			if parts := strings.Split(name, "."); len(parts) == 2 {
				sub, ok := ft.FieldByName(parts[1])
				if !ok {
					t.Fatalf("no field %s", name)
				}
				path, ft = append(append([]int{}, path...), sub.Index...), sub.Type
			}
			leaves(path, ft, func(leaf []int) {
				p := *base
				if err := flip(reflect.ValueOf(&p).Elem().FieldByIndex(leaf)); err != nil {
					t.Errorf("%s %v: %v", name, leaf, err)
					return
				}
				if _, err := p.Confirmed(p.Needs); err == nil || !strings.Contains(err.Error(), "not produced by Preview") {
					t.Errorf("%s %v edited: Confirmed err %v, want refused as edited", name, leaf, err)
				}
			})
		}
	}
}
