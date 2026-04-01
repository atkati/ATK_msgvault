package store

import (
	"path/filepath"
	"testing"
)

func openTagTestStore(t *testing.T) *Store {
	t.Helper()
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "msgvault.db")
	st, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.InitSchema(); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	return st
}

func TestCreateUserTag(t *testing.T) {
	st := openTagTestStore(t)

	id1, err := st.CreateUserTag("SENSIBLE", "#ff0000")
	if err != nil {
		t.Fatalf("CreateUserTag: %v", err)
	}
	if id1 == 0 {
		t.Fatal("expected non-zero tag ID")
	}

	// Creating the same tag again should return the same ID.
	id2, err := st.CreateUserTag("SENSIBLE", "#ff0000")
	if err != nil {
		t.Fatalf("CreateUserTag (duplicate): %v", err)
	}
	if id2 != id1 {
		t.Fatalf("expected same ID %d, got %d", id1, id2)
	}
}

func TestCreateUserTag_EmptyName(t *testing.T) {
	st := openTagTestStore(t)

	_, err := st.CreateUserTag("", "")
	if err == nil {
		t.Fatal("expected error for empty tag name")
	}
}

func TestGetUserTag(t *testing.T) {
	st := openTagTestStore(t)

	_, err := st.CreateUserTag("Important", "#0000ff")
	if err != nil {
		t.Fatalf("CreateUserTag: %v", err)
	}

	tag, err := st.GetUserTag("Important")
	if err != nil {
		t.Fatalf("GetUserTag: %v", err)
	}
	if tag == nil {
		t.Fatal("expected tag, got nil")
	}
	if tag.Name != "Important" {
		t.Fatalf("expected name 'Important', got %q", tag.Name)
	}

	// Non-existent tag.
	tag2, err := st.GetUserTag("NonExistent")
	if err != nil {
		t.Fatalf("GetUserTag: %v", err)
	}
	if tag2 != nil {
		t.Fatalf("expected nil for non-existent tag, got %v", tag2)
	}
}

func TestListUserTagsWithCount(t *testing.T) {
	st := openTagTestStore(t)

	_, err := st.CreateUserTag("Alpha", "")
	if err != nil {
		t.Fatalf("CreateUserTag: %v", err)
	}
	_, err = st.CreateUserTag("Beta", "#00ff00")
	if err != nil {
		t.Fatalf("CreateUserTag: %v", err)
	}

	tags, err := st.ListUserTagsWithCount()
	if err != nil {
		t.Fatalf("ListUserTagsWithCount: %v", err)
	}
	if len(tags) != 2 {
		t.Fatalf("expected 2 tags, got %d", len(tags))
	}
	if tags[0].Name != "Alpha" || tags[1].Name != "Beta" {
		t.Fatalf("expected [Alpha, Beta], got [%s, %s]", tags[0].Name, tags[1].Name)
	}
	if tags[0].MessageCount != 0 {
		t.Fatalf("expected 0 messages, got %d", tags[0].MessageCount)
	}
}

func TestDeleteUserTag(t *testing.T) {
	st := openTagTestStore(t)

	_, err := st.CreateUserTag("ToDelete", "")
	if err != nil {
		t.Fatalf("CreateUserTag: %v", err)
	}

	err = st.DeleteUserTag("ToDelete")
	if err != nil {
		t.Fatalf("DeleteUserTag: %v", err)
	}

	tag, err := st.GetUserTag("ToDelete")
	if err != nil {
		t.Fatalf("GetUserTag: %v", err)
	}
	if tag != nil {
		t.Fatal("expected tag to be deleted")
	}
}

func TestDeleteUserTag_NotFound(t *testing.T) {
	st := openTagTestStore(t)

	err := st.DeleteUserTag("Ghost")
	if err == nil {
		t.Fatal("expected error for non-existent tag")
	}
}

func TestTagAndUntagMessage(t *testing.T) {
	st := openTagTestStore(t)

	// Create a source and a message to tag.
	src, err := st.GetOrCreateSource("test", "test@example.com")
	if err != nil {
		t.Fatalf("GetOrCreateSource: %v", err)
	}
	convID, err := st.EnsureConversation(src.ID, "thread-1", "Test Thread")
	if err != nil {
		t.Fatalf("EnsureConversation: %v", err)
	}

	msgID, err := st.PersistMessage(&MessagePersistData{
		Message: &Message{
			ConversationID:  convID,
			SourceID:        src.ID,
			SourceMessageID: "test-msg-1",
			MessageType:     "email",
		},
	})
	if err != nil {
		t.Fatalf("PersistMessage: %v", err)
	}

	// Tag the message.
	err = st.TagMessage(msgID, "BetterVTC")
	if err != nil {
		t.Fatalf("TagMessage: %v", err)
	}

	// Verify tag is applied.
	ids, err := st.SearchMessagesByTag("BetterVTC")
	if err != nil {
		t.Fatalf("SearchMessagesByTag: %v", err)
	}
	if len(ids) != 1 || ids[0] != msgID {
		t.Fatalf("expected [%d], got %v", msgID, ids)
	}

	// Verify tag count.
	tags, err := st.ListUserTagsWithCount()
	if err != nil {
		t.Fatalf("ListUserTagsWithCount: %v", err)
	}
	if len(tags) != 1 || tags[0].MessageCount != 1 {
		t.Fatalf("expected 1 tag with 1 message, got %v", tags)
	}

	// Untag.
	err = st.UntagMessage(msgID, "BetterVTC")
	if err != nil {
		t.Fatalf("UntagMessage: %v", err)
	}

	ids2, err := st.SearchMessagesByTag("BetterVTC")
	if err != nil {
		t.Fatalf("SearchMessagesByTag after untag: %v", err)
	}
	if len(ids2) != 0 {
		t.Fatalf("expected empty after untag, got %v", ids2)
	}
}

func TestTagMessage_Idempotent(t *testing.T) {
	st := openTagTestStore(t)

	src, err := st.GetOrCreateSource("test", "test@example.com")
	if err != nil {
		t.Fatalf("GetOrCreateSource: %v", err)
	}
	convID, err := st.EnsureConversation(src.ID, "thread-1", "Test")
	if err != nil {
		t.Fatalf("EnsureConversation: %v", err)
	}

	msgID, err := st.PersistMessage(&MessagePersistData{
		Message: &Message{
			ConversationID:  convID,
			SourceID:        src.ID,
			SourceMessageID: "test-msg-2",
			MessageType:     "email",
		},
	})
	if err != nil {
		t.Fatalf("PersistMessage: %v", err)
	}

	// Tag twice — should not error or create duplicates.
	if err := st.TagMessage(msgID, "Dup"); err != nil {
		t.Fatalf("TagMessage (first): %v", err)
	}
	if err := st.TagMessage(msgID, "Dup"); err != nil {
		t.Fatalf("TagMessage (second): %v", err)
	}

	ids, err := st.SearchMessagesByTag("Dup")
	if err != nil {
		t.Fatalf("SearchMessagesByTag: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("expected 1 message, got %d", len(ids))
	}
}
