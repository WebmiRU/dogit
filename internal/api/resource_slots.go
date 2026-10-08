package api

import (
	"context"
	"fmt"

	"github.com/ewolf/dogit/internal/models"
)

// fillResourceSlots answers every need a module declared, and returns what to hand it.
//
// One pass per need, in the order the module declared them, and the order matters: a module
// that wants a database and a store and has one described for each takes both, and the pairing
// is by slot rather than by order of arrival. A module that has two needs of the same kind is
// the case that makes this necessary rather than tidy — "the first store" and "the second
// store" are not something a module can ask for by describing what is in them.
//
// Four steps per need, in this order:
//
//   - what it already holds for that slot is handed back, and nothing is touched. Re-registering
//     is normal and must not move anything: a module that restarts comes back with a new address
//     and finds its resource exactly where it left it.
//   - a free resource an administrator described for this kind of module is given, before
//     anything is created. This is the whole of "let a module have its own": somebody writes
//     down a database on a host they already have, and the next module of that kind is given it
//     rather than a new one inside this cluster.
//   - a required database this instance can make is made, because a module that declares it
//     needs one and has none has nothing to work with.
//   - anything else is left empty, which is the answer for an optional need and the honest
//     answer for a required one this instance cannot make.
//
// A resource whose slot the module no longer declares is given up on the way past. A module
// that changed its mind about a need must not keep the thing it was given for it: the resource
// is still here, with nobody holding it, and the module that no longer asks for it does not go
// on being counted as holding it.
func (s *Server) fillResourceSlots(ctx context.Context,
	integration *models.Integration) (map[string]any, error) {
	needs := integration.Capabilities.Needs()
	if len(needs) == 0 {
		return nil, nil
	}

	// What this module already holds, by slot. Read once: a registration asks several questions
	// and asking the database once per need is a way of being told different answers.
	// What this module already holds, by slot. Read once: a registration asks several questions
	// and asking the database once per need is a way of being told different answers.
	//
	// A failure here stops the whole pass rather than being treated as "nothing held". An
	// empty list is an answer — it says every need below should be filled — and answering it
	// on the strength of a failed read is how a module that already has a database is given a
	// second one, with the first left orphaned and holding every deployment it ever ran.
	own, err := s.store.Resources().ByModule(ctx, integration.ID)
	if err != nil {
		return nil, fmt.Errorf("read the resources this module holds: %w", err)
	}

	held := map[string]*models.Resource{}
	freed := []string{}
	for _, one := range own {
		if !slotWanted(needs, one.NeedKey) {
			// The module no longer declares this slot. Released rather than dropped, so the
			// resource is still here for somebody to attach — and because this is a module
			// changing its manifest, not somebody deleting a database.
			released, err := s.store.Resources().ReleaseByID(ctx, one.ID)
			if err != nil {
				s.log.Warn("a resource for a slot the module no longer declares could not be given up",
					"resource", one.Coordinate(), "slot", one.NeedKey, "error", err)
				continue
			}
			freed = append(freed, released.Coordinate())
			continue
		}
		if one.NeedKey != "" {
			held[one.NeedKey] = &one
			continue
		}
		// Held without a named slot: a module from before slots existed, with one resource.
		// It answers the first need it could, which is the only one it could have had.
		if _, taken := held[""]; !taken {
			held[""] = &one
		}
	}
	for _, coordinate := range freed {
		s.log.Info("a resource was given up because the module no longer asks for it",
			"kind", integration.Kind, "resource", coordinate)
	}

	out := map[string]any{}
	for _, need := range needs {
		// An empty slot key means a module that predates slots, and it holds exactly one
		// resource. Filled once; the rest of its needs are answered the ordinary way.
		if one, ok := held[need.Key]; ok {
			out[need.Key] = resourceHandover(*one)
			continue
		}
		if one, ok := held[""]; ok && need.Key == firstSlot(needs) {
			out[need.Key] = resourceHandover(*one)
			continue
		}

		adopted := s.adoptedResource(ctx, integration, need)
		if adopted != nil {
			if err := s.store.Resources().Grant(ctx, adopted.ID, integration.ID, need.Key); err != nil {
				s.log.Warn("a described resource could not be given to the module",
					"kind", integration.Kind, "slot", need.Key, "error", err)
				out[need.Key] = nil
				continue
			}
			s.log.Info("the module was given the resource an administrator described",
				"kind", integration.Kind, "slot", need.Key,
				"resource", adopted.Coordinate(), "name", adopted.Name)
			granted, err := s.store.Resources().ByID(ctx, adopted.ID)
			if err != nil {
				s.log.Warn("the resource was given but could not be read back",
					"kind", integration.Kind, "slot", need.Key, "error", err)
				out[need.Key] = nil
				continue
			}
			out[need.Key] = resourceHandover(*granted)
			continue
		}

		// Only a database is made here. An object store is never created by this instance —
		// it would be a promise this code cannot keep, and the answer to "a module wants a
		// store" is a resource an administrator described.
		if need.Kind != ResourceKindDatabase || !need.Required {
			out[need.Key] = nil
			continue
		}

		provisioned, err := s.store.Integrations().ProvisionModuleDatabase(ctx, s.cfg.DatabaseURL, integration.Kind)
		if err != nil {
			return nil, err
		}
		if err := s.store.Integrations().SetModuleDatabase(ctx, integration.ID,
			provisioned.Name, provisioned.Role); err != nil {
			s.log.Warn("the database was created but its name could not be recorded",
				"kind", integration.Kind, "error", err)
		}

		// And the same database as a resource, so that what this instance gave away is on a
		// list of what this instance gave away. The password is written sealed, from the only
		// copy there will be: this is the moment the module is told, and afterwards nobody has
		// it.
		written, err := s.store.Resources().Put(ctx, models.Resource{
			Kind:                ResourceKindDatabase,
			Software:            DatabaseSoftware,
			Name:                provisioned.Name,
			Origin:              models.OriginManaged,
			Parts:               provisioned.Parts,
			IntegrationID:       &integration.ID,
			NeedKey:             need.Key,
			LastIntegrationKind: integration.Kind,
		})
		if err != nil {
			s.log.Warn("the database was created but not recorded as a resource",
				"kind", integration.Kind, "error", err)
		}
		s.log.Info("provisioned a database for the module",
			"kind", integration.Kind, "slot", need.Key, "database", provisioned.Name)

		handover := map[string]any{
			"kind":    ResourceKindDatabase,
			"name":    provisioned.Name,
			"payload": descriptorOf(provisioned.Parts),
			// The role is the user a database this instance made has, and it is already in
			// the payload under its own name. Kept beside it as well because it is the one part
			// an administrator is asked for by name when the database has to be dropped by
			// hand.
			"role": provisioned.Role,
		}
		if written != nil {
			handover["id"] = written.ID
		}
		out[need.Key] = handover
	}
	return out, nil
}

// slotWanted says whether the module still declares this slot.
func slotWanted(needs []models.ResourceNeed, key string) bool {
	for _, need := range needs {
		if need.Key == key {
			return true
		}
	}
	return false
}

func firstSlot(needs []models.ResourceNeed) string {
	if len(needs) == 0 {
		return ""
	}
	return needs[0].Key
}

// descriptorOf is the payload for a set of parts this instance has just made and therefore
// already holds in the clear.
//
// Not read back out of the store: this is the moment the database was created, so every part
// of it is in hand, and going back to unseal a password that was never sealed would be work
// done to be able to say something already known.
func descriptorOf(parts map[string]string) map[string]string {
	out := make(map[string]string, len(parts))
	for key, value := range parts {
		if value != "" {
			out[key] = value
		}
	}
	return out
}
