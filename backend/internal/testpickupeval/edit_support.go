package testpickupeval

import (
	"time"

	"restaurant-assistant/backend/internal/cart"
)

func (f *fixture) edit(spec Edit) EditObservation {
	f.t.Helper()
	before, err := f.store.Read(f.ctx, f.local, f.snapshot.ID)
	f.check(err)
	var action cart.Action
	var text string
	switch spec.Kind {
	case AddSandwich:
		menu, parseErr := cart.ParseMenuID(menuID)
		f.check(parseErr)
		action, err = cart.Add(menu, 1)
		text = "Add one fictional sandwich"
	case NoteFirstUnit:
		if len(before.Units) == 0 {
			f.t.Fatal("note edit needs a stored unit")
		}
		action, err = cart.SetNote([]cart.UnitID{before.Units[0].ID}, spec.Note)
		text = "Change the first sandwich note"
	default:
		f.t.Fatal("unsupported fixture edit")
	}
	f.check(err)
	claim := f.claimInbound(text)
	call, err := cart.EditCall(before.ID, before.Version, action)
	f.check(err)
	ref, err := f.store.AdmitNativeEdit(f.ctx, f.local, claim, 1, call)
	f.check(err)
	out := EditObservation{Kind: spec.Kind, SourceID: claim.Source().EventUUID(), OperationID: ref.OperationID(), Pending: spec.Pending, Before: before}
	if !spec.Pending {
		// Recover the actual admitted reference using its independent business
		// lease. A menu claim passed to legacy Execute would not be valid here.
		work, ok, err := f.store.RecoverNativeEdit(f.ctx, ref, time.Minute)
		f.check(err)
		if !ok || work.Ref().OperationID() != ref.OperationID() {
			f.t.Fatal("admitted native work was not actually claimed")
		}
		out.Result, out.Error = f.store.ExecuteNativeEdit(f.ctx, work)
		f.check(out.Error)
	}
	out.After, err = f.store.Read(f.ctx, f.local, before.ID)
	f.check(err)
	f.snapshot = out.After
	// Admission provenance is real even when the reply job is retired; native
	// business work has its own lease and remains pending until explicitly run.
	f.finish(f.execution(claim), "Local edit interaction recorded (not presented)")
	return out
}
