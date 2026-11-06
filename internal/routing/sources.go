package routing

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"eggbox/internal/ruleurl"
)

// Errors of the operations on an item's sources.
var (
	ErrNoSource        = errors.New("there is no such source")
	ErrLastSource      = errors.New("an item needs at least one source; delete the item instead")
	ErrDuplicateSource = errors.New("the item already has this source")
	ErrNotInline       = errors.New("only hand-written entries can be edited")
	ErrNoEntries       = errors.New("there is nothing to add")
	ErrTooManyEntries  = fmt.Errorf("a source can hold at most %d hand-written entries; use a rule file for more", MaxEntries)
)

// MaxEntries bounds one hand-written source.
const MaxEntries = 20000

func (s Source) clone() Source {
	s.Entries = append([]Entry(nil), s.Entries...)
	return s
}

// sourceKey is what makes two sources the same one: a file by its address, a
// hand-written source by its identity.
func sourceKey(s Source) string {
	if s.Kind == SourceURL {
		return "url:" + s.URL
	}
	return "inline:" + s.ID
}

// checkedSource validates and normalizes a source about to be added: a file
// must have a web address, hand-written entries must be valid and not empty.
func checkedSource(src Source) (Source, error) {
	switch src.Kind {
	case SourceURL:
		info, err := ruleurl.Canonical(src.URL)
		if err != nil {
			return Source{}, err
		}
		src.URL = info.URL
		src.ID = sourceID(info.URL)
		src.Entries = nil
		if src.Project == "" {
			src.Project = info.Project
		}
		if src.Name == "" {
			src.Name = info.Name
		}
	case SourceInline:
		entries, err := normalizedEntries(src.Entries)
		if err != nil {
			return Source{}, err
		}
		src.Entries = entries
		src.URL = ""
		if src.ID == "" {
			src.ID = NewID("n")
		}
	default:
		return Source{}, fmt.Errorf("unknown source type %q", src.Kind)
	}
	return src, nil
}

// normalizedEntries validates entries and returns them in the form the
// generator expects, without repeats.
func normalizedEntries(in []Entry) ([]Entry, error) {
	if len(in) == 0 {
		return nil, ErrNoEntries
	}
	if len(in) > MaxEntries {
		return nil, ErrTooManyEntries
	}
	seen := map[Entry]bool{}
	var out []Entry
	for _, e := range in {
		if err := e.Valid(); err != nil {
			return nil, err
		}
		e = e.Normalize()
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return out, nil
}

// editItemErr is editItem for changes that can fail: own works on an item the
// user owns, over on an item the plan owns, through the overlay.
func (s *State) editItemErr(id string, own func(*Item) error, over func(*Overlay, Item) error) error {
	mine, ov, base := s.activeEdit()
	if mine != nil {
		for i := range mine.Items {
			if mine.Items[i].ID == id {
				return own(&mine.Items[i])
			}
		}
		return ErrNoItem
	}
	for i := range ov.Items {
		if ov.Items[i].ID == id {
			return own(&ov.Items[i])
		}
	}
	for _, it := range base.Items {
		if it.ID == id {
			return over(ov, it)
		}
	}
	return ErrNoItem
}

// AddSource gives an item more rules: a rule file, or hand-written entries.
// On an item of a plan that is kept up to date elsewhere the source is laid
// over the plan, so it survives a refresh and follows the item to another plan
// that has one of the same name.
func (s *State) AddSource(itemID string, src Source) error {
	src, err := checkedSource(src)
	if err != nil {
		return err
	}
	return s.editItemErr(itemID,
		func(it *Item) error {
			if src.Kind == SourceURL && hasURL(it.Sources, src.URL) {
				return ErrDuplicateSource
			}
			it.Sources = append(it.Sources, src)
			return nil
		},
		func(ov *Overlay, it Item) error {
			if src.Kind == SourceURL && (hasURL(it.Sources, src.URL) || hasURL(ov.Extra[primary(it)], src.URL)) {
				return ErrDuplicateSource
			}
			if ov.Extra == nil {
				ov.Extra = map[string][]Source{}
			}
			ov.Extra[primary(it)] = append(ov.Extra[primary(it)], src)
			return nil
		})
}

func hasURL(srcs []Source, url string) bool {
	return slices.ContainsFunc(srcs, func(s Source) bool { return s.Kind == SourceURL && s.URL == url })
}

// RemoveSource takes a source away from an item. The sources a plan itself
// gives an item cannot be removed; the ones the user added can. An item the
// user owns keeps at least one.
func (s *State) RemoveSource(itemID, sourceID string) error {
	return s.editItemErr(itemID,
		func(it *Item) error {
			i := slices.IndexFunc(it.Sources, func(x Source) bool { return x.ID == sourceID })
			if i < 0 {
				return ErrNoSource
			}
			if len(it.Sources) == 1 {
				return ErrLastSource
			}
			// Not in place: a view taken earlier shares the array.
			it.Sources = append(slices.Clone(it.Sources[:i]), it.Sources[i+1:]...)
			return nil
		},
		func(ov *Overlay, it Item) error {
			key := primary(it)
			list := ov.Extra[key]
			i := slices.IndexFunc(list, func(x Source) bool { return x.ID == sourceID })
			if i < 0 {
				if slices.ContainsFunc(it.Sources, func(x Source) bool { return x.ID == sourceID }) {
					return ErrLocked
				}
				return ErrNoSource
			}
			list = slices.Delete(list, i, i+1)
			if len(list) == 0 {
				delete(ov.Extra, key)
			} else {
				ov.Extra[key] = list
			}
			return nil
		})
}

// SetEntries replaces what a hand-written source holds.
func (s *State) SetEntries(itemID, sourceID string, entries []Entry) error {
	entries, err := normalizedEntries(entries)
	if err != nil {
		return err
	}
	set := func(list []Source) error {
		i := slices.IndexFunc(list, func(x Source) bool { return x.ID == sourceID })
		if i < 0 {
			return ErrNoSource
		}
		if list[i].Kind != SourceInline {
			return ErrNotInline
		}
		list[i].Entries = entries
		return nil
	}
	return s.editItemErr(itemID,
		func(it *Item) error {
			// Not in place: a view taken earlier shares the array.
			it.Sources = cloneSources(it.Sources)
			return set(it.Sources)
		},
		func(ov *Overlay, it Item) error {
			err := set(ov.Extra[primary(it)])
			if errors.Is(err, ErrNoSource) && slices.ContainsFunc(it.Sources, func(x Source) bool { return x.ID == sourceID }) {
				return ErrLocked
			}
			return err
		})
}

// RenameItem renames an item the user owns.
func (s *State) RenameItem(itemID, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrNoName
	}
	for _, r := range s.View().Rows {
		if r.Item.ID != itemID && slices.Contains(r.Item.Names(), NormName(name)) {
			return ErrDuplicateName
		}
	}
	return s.editItemErr(itemID,
		func(it *Item) error { it.Name = name; return nil },
		func(*Overlay, Item) error { return ErrLocked })
}

// SetItemIcon changes the icon of an item the user owns.
func (s *State) SetItemIcon(itemID, icon string) error {
	return s.editItemErr(itemID,
		func(it *Item) error { it.Icon = icon; return nil },
		func(*Overlay, Item) error { return ErrLocked })
}

// CopyItemToMine copies an item of the active plan, as the person sees it
// (its sources, destination and switch), to the end of a plan of their own
// that is not the one in force.
func (s *State) CopyItemToMine(itemID, planID string) (Item, error) {
	row, _, ok := s.View().Row(itemID)
	if !ok {
		return Item{}, ErrNoItem
	}
	target := s.mineOf(planID)
	if target == nil || target.ID == s.ActiveID() {
		return Item{}, ErrNotMine
	}
	it := row.Item.Clone()
	// The key stays: it is what keeps the name of a shipped item in the
	// language the person reads.
	it.ID, it.Anchor = NewID("i"), ""
	for _, other := range target.Items {
		if SameName(other, it) {
			return Item{}, ErrDuplicateName
		}
	}
	target.Items = append(target.Items, it)
	return it, nil
}
