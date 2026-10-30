package memory

import (
	"context"
	"testing"

	"blavingad/internal/store"
)

func TestSaveSearchAndDedupe(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := New(st, nil, "")
	ctx := context.Background()
	a, err := m.Save(ctx, Memory{Kind: "fact", Content: "Le site de Fold est déployé sur Cloudflare Workers"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := m.Save(ctx, Memory{Kind: "fact", Content: "Le site de Fold est déployé sur Cloudflare Workers"})
	if !b.Merged || b.ID != a.ID {
		t.Errorf("exact duplicate should merge: %+v", b)
	}
	m.Save(ctx, Memory{Kind: "preference", Subject: "Alice", Content: "Alice préfère le thé au café"})
	res := m.Search(ctx, "deploiement cloudflare", "", 5)
	if len(res) == 0 || res[0].ID != a.ID {
		t.Fatalf("accent-insensitive search failed: %+v", res)
	}
	if got := m.Search(ctx, "thé", "fact", 5); len(got) != 0 {
		t.Errorf("kind filter ignored: %+v", got)
	}
}

func TestPersonNotes(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	m := New(st, nil, "")
	m.UpsertPerson(Person{SlackID: "U1", Name: "alice"})
	m.AppendPersonNote("U1", "aime Go")
	m.AppendPersonNote("U1", "aime Go")
	p, _ := m.GetPerson("U1")
	if p.Notes != "- aime Go" {
		t.Errorf("notes = %q", p.Notes)
	}
}
