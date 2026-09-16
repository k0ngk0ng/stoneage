package aigame

import "testing"

func TestChatContextSeparatesConnectionsAndCharacterLogins(t *testing.T) {
	first := newGameState(true)
	second := newGameState(true)
	first.appendChat(ChatMessage{FromID: 42, Text: "one"})
	second.appendChat(ChatMessage{FromID: 42, Text: "two"})
	initial := first.chat[0].ContextID
	if len(initial) != 32 || initial == second.chat[0].ContextID {
		t.Fatal("connections share durable chat context")
	}
	applyEventLocked(&first, stringEvent("CharLogin", "successful"))
	first.appendChat(ChatMessage{FromID: 42, Text: "three"})
	login := first.chat[1].ContextID
	if login == initial || len(login) != 32 || first.chat[0].ContextID != initial {
		t.Fatal("login relabeled historical chat")
	}
	first.appendChat(ChatMessage{FromID: 43, Text: "four"})
	if first.chat[2].ContextID != login {
		t.Fatal("same observation session lost context")
	}
	applyEventLocked(&first, stringEvent("CharLogout", "successful"))
	first.appendChat(ChatMessage{FromID: 42, Text: "five"})
	if first.chat[3].ContextID != "" {
		t.Fatal("logout retained attribution context")
	}
	applyEventLocked(&first, stringEvent("CharLogin", "successful"))
	first.appendChat(ChatMessage{FromID: 42, Text: "six"})
	if first.chat[4].ContextID == login || first.chat[4].ContextID == initial || len(first.chat[4].ContextID) != 32 {
		t.Fatal("relogin reused a context")
	}
}
