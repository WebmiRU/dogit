package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/modulechan"
)

// Asking a module a question over the channel and waiting for what it says.
//
// The properties are all about two connections talking at once, so these stand up a real server
// and dial it. A handler-level test cannot see them: a module's answer arrives on the same
// socket its command went out on, and the whole question is whether the right waiter is found.

// askOnChannel calls the fixture's module and answers on the module's side of the socket.
//
// Returns what the core got back. The answering side runs on its own goroutine because the core
// is waiting for it, and a test that answered inline would deadlock on its first line.
func askOnChannel(t *testing.T, f *moduleFixture, conn *websocket.Conn,
	decision Decision, reply func(modulechan.Message) any, refuse string) []byte {

	t.Helper()

	answered := make(chan []byte, 1)
	go func() {
		command := readCommand(t, conn)
		if command.ID == "" {
			t.Errorf("the command arrived with no id, so nothing can be waiting for it")
			answered <- nil
			return
		}
		if reply == nil {
			write(t, conn, modulechan.Message{ID: command.ID, Kind: modulechan.Answer,
				Token: f.moduleToken})
		} else {
			payload, err := json.Marshal(reply(command))
			if err != nil {
				t.Errorf("encode the answer: %v", err)
				answered <- nil
				return
			}
			write(t, conn, modulechan.Message{ID: command.ID, Kind: modulechan.Answer,
				Token: f.moduleToken, Payload: payload})
		}
		answered <- command.Payload
	}()

	got, err := f.server.moduleChannel().Call(context.Background(), f.module, decision, 5*time.Second)
	<-answered
	if err != nil {
		return nil
	}
	return got
}

// The whole point of the call: a question goes out with an id and the answer comes back with the
// same one, and the caller is holding the answer.
func TestAModuleAnswersAQuestionAskedOverTheChannel(t *testing.T) {
	f := newModuleFixture(t)
	conn := comeOnline(t, f)

	got := askOnChannel(t, f, conn, Decision{
		Kind:    modulechan.ResolveImage,
		Payload: map[string]any{"image": "grp/prj", "tag": "v1"},
	}, func(command modulechan.Message) any {
		var asked struct {
			Image string `json:"image"`
			Tag   string `json:"tag"`
		}
		if err := command.PayloadInto(&asked); err != nil {
			t.Errorf("the command's payload: %v", err)
		}
		return map[string]any{"image": asked.Image, "tag": asked.Tag, "digest": "sha256:abc"}
	}, "")

	if len(got) == 0 {
		t.Fatal("the caller got nothing back")
	}
	var answer struct {
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(got, &answer); err != nil {
		t.Fatalf("read the answer: %v", err)
	}
	if answer.Digest != "sha256:abc" {
		t.Errorf("the caller was told %q, want the digest the module gave", answer.Digest)
	}
}

// A refusal and a silence are different things, and one of them is somebody's decision while the
// other is nothing at all. This is the one that has a sentence in it.
func TestAModuleMayRefuseAQuestion(t *testing.T) {
	f := newModuleFixture(t)
	conn := comeOnline(t, f)

	go func() {
		command := readCommand(t, conn)
		payload, _ := json.Marshal(map[string]any{
			"error": map[string]any{"message": "not allowed to pull grp/prj"},
		})
		write(t, conn, modulechan.Message{ID: command.ID, Kind: modulechan.Answer,
			Token: f.moduleToken, Payload: payload})
	}()

	answer, err := f.server.moduleChannel().Call(context.Background(), f.module,
		Decision{Kind: modulechan.ResolveImage, Payload: map[string]any{"image": "grp/prj"}},
		5*time.Second)
	if err != nil {
		t.Fatalf("a refusal is an answer, not a failure: %v", err)
	}

	refusal := moduleRefusalOf(f.module, "what that tag points at", answer, nil)
	if refusal == nil {
		t.Fatal("a refusal was read as an answer")
	}
	if !strings.Contains(refusal.Error(), "not allowed to pull grp/prj") {
		t.Errorf("the refusal does not carry the module's own sentence: %v", refusal)
	}
}

// Silence has to end, or a caller waits for a module that is not going to answer.
func TestAQuestionNobodyAnswersRunsOut(t *testing.T) {
	f := newModuleFixture(t)
	comeOnline(t, f)

	answer, err := f.server.moduleChannel().Call(context.Background(), f.module,
		Decision{Kind: modulechan.ResolveImage, Payload: map[string]any{"image": "grp/prj"}},
		150*time.Millisecond)

	if !errors.Is(err, ErrNoAnswer) {
		t.Fatalf("calling a module that says nothing: %v, want ErrNoAnswer", err)
	}
	if answer != nil {
		t.Errorf("a caller that got no answer was given %q", answer)
	}

	// And it says so in words nobody will mistake for the module having refused.
	refusal := moduleRefusalOf(f.module, "what that tag points at", answer, err)
	if !errors.Is(refusal, ErrNoAnswer) {
		t.Error("the sentence lost the distinction between a refusal and a silence")
	}
	if strings.Contains(refusal.Error(), "refused") {
		t.Errorf("a silence was reported as a refusal: %v", refusal)
	}
}

// A caller that gave up must not be waited for by the module's read loop, and must not be
// confused with whoever asks next: the two are told apart by the id, and the id has to be dropped
// when nobody is left waiting on it.
func TestACallerThatGaveUpIsForgotten(t *testing.T) {
	f := newModuleFixture(t)
	channel := f.server.moduleChannel()

	if _, err := channel.Call(context.Background(), f.module,
		Decision{Kind: modulechan.ResolveImage}, 100*time.Millisecond); !errors.Is(err, ErrNoAnswer) {
		t.Fatalf("want ErrNoAnswer, got %v", err)
	}
	if got := channel.PendingAnswers(); got != 0 {
		t.Fatalf("the core is still holding %d waiters for a caller that gave up", got)
	}

	// And an answer arriving afterwards for that command is somebody's dropped answer rather
	// than a crash: it is not found, and the read loop carries on.
	if answered := channel.answerTo(uuid.NewString(), json.RawMessage(`{"digest":"x"}`)); answered {
		t.Error("an answer for an id nobody is waiting on was delivered")
	}
}

// A command asked as a question is still kept, because the module might not have been listening.
// The caller here gives up; the command does not go with it.
func TestAQuestionIsHeldEvenWhenTheCallerGivesUp(t *testing.T) {
	f := newModuleFixture(t)
	channel := f.server.moduleChannel()
	giveLifetime(t, f, 300)

	if _, err := channel.Call(context.Background(), f.module,
		Decision{Kind: modulechan.ResolveImage, Payload: map[string]any{"image": "grp/prj"}},
		100*time.Millisecond); !errors.Is(err, ErrNoAnswer) {
		t.Fatalf("want ErrNoAnswer, got %v", err)
	}

	if got := channel.PendingCommands(f.module.ID); got != 1 {
		t.Fatalf("the core is holding %d commands after the caller gave up, want the question", got)
	}
}

// A module with no lifetime set holds nothing, questions included. A question is a decision like
// any other: the setting is about what the core keeps, not about what it asks.
func TestAQuestionIsNotHeldWhenTheLifetimeIsZero(t *testing.T) {
	f := newModuleFixture(t)
	channel := f.server.moduleChannel()
	giveLifetime(t, f, 0)

	if _, err := channel.Call(context.Background(), f.module,
		Decision{Kind: modulechan.ResolveImage}, 100*time.Millisecond); !errors.Is(err, ErrNoAnswer) {
		t.Fatalf("want ErrNoAnswer, got %v", err)
	}
	if got := channel.PendingCommands(f.module.ID); got != 0 {
		t.Fatalf("with no lifetime the core is holding %d questions", got)
	}
}

// Two questions at once must not be confused for one another, which is what the id is for.
func TestTwoQuestionsAtOnceAreAnsweredToTheRightCaller(t *testing.T) {
	f := newModuleFixture(t)
	conn := comeOnline(t, f)

	// The module answers each command with the tag it was asked about, so an answer that
	// reached the wrong caller is visible as a wrong digest rather than as a mystery.
	go func() {
		for range 2 {
			command := readCommand(t, conn)
			var asked struct {
				Tag string `json:"tag"`
			}
			if err := command.PayloadInto(&asked); err != nil {
				t.Errorf("the command's payload: %v", err)
			}
			payload, _ := json.Marshal(map[string]any{"digest": "sha256:" + asked.Tag})
			write(t, conn, modulechan.Message{ID: command.ID, Kind: modulechan.Answer,
				Token: f.moduleToken, Payload: payload})
		}
	}()

	channel := f.server.moduleChannel()
	tags := []string{"one", "two"}
	answers := make(chan string, len(tags))

	for _, tag := range tags {
		go func(tag string) {
			answer, err := channel.Call(context.Background(), f.module, Decision{
				Kind:    modulechan.ResolveImage,
				Payload: map[string]any{"tag": tag},
			}, 5*time.Second)
			if err != nil {
				t.Errorf("the call for %q: %v", tag, err)
				answers <- ""
				return
			}
			var said struct {
				Digest string `json:"digest"`
			}
			if err := json.Unmarshal(answer, &said); err != nil {
				t.Errorf("the answer for %q: %v", tag, err)
			}
			answers <- said.Digest
		}(tag)
	}

	for range tags {
		got := <-answers
		if got != "sha256:one" && got != "sha256:two" {
			t.Errorf("a caller was given %q, which belongs to neither question", got)
		}
	}
}

// The success case, which is the one that has to exist: a function that turns every answer into
// an error turns every question the core asks into a failed one, and no test that only looks at
// refusals and timeouts will notice that it has happened.
func TestAnAnswerIsNotAFailure(t *testing.T) {
	f := newModuleFixture(t)

	if refusal := moduleRefusalOf(f.module, "what that tag points at",
		json.RawMessage(`{"digest":"sha256:abc"}`), nil); refusal != nil {
		t.Errorf("an answer was read as a failure: %v", refusal)
	}
}

// What a module receives is the object the core meant to send.
//
// This is the shape of a bug that costs more than it looks. A caller that hands Call bytes of
// JSON it built itself gets those bytes encoded again — as base64, inside a JSON string — and
// the module answers "this is not a question I can read". The refusal is true, names the shape
// rather than the mistake, and sends the caller looking for a version mismatch that does not
// exist. On this instance it meant every deployment was applied by a mutable tag instead of a
// digest, so the Deployment never changed between runs and no rollout ever happened.
func TestAPayloadOfBytesArrivesAsTheObjectItWas(t *testing.T) {
	built, err := json.Marshal(map[string]string{"project": "test/versions", "tag": "abc1234"})
	if err != nil {
		t.Fatalf("build the payload: %v", err)
	}

	sent, err := modulechan.Payload(built)
	if err != nil {
		t.Fatalf("payloadOf: %v", err)
	}
	if string(sent) != string(built) {
		t.Fatalf("the payload changed on the way out:\n got %s\nwant %s", sent, built)
	}

	// And what the module would make of it, which is the question that matters: an object
	// with the fields in it, not a string.
	var asked struct {
		Project string `json:"project"`
		Tag     string `json:"tag"`
	}
	if err := json.Unmarshal(sent, &asked); err != nil {
		t.Fatalf("the module could not read the payload: %v", err)
	}
	if asked.Project != "test/versions" || asked.Tag != "abc1234" {
		t.Errorf("the payload arrived as %#v", asked)
	}
}

// RawMessage and bytes mean the same thing, and a value still gets encoded. All three forms are
// in use by callers in this package and none of them is a mistake.
func TestEveryPayloadFormArrivesReadable(t *testing.T) {
	want := map[string]string{"image": "registry.test/versions"}

	for name, payload := range map[string]any{
		"a value":     want,
		"raw message": json.RawMessage(`{"image":"registry.test/versions"}`),
		"bytes":       []byte(`{"image":"registry.test/versions"}`),
		"nothing":     nil,
	} {
		t.Run(name, func(t *testing.T) {
			sent, err := modulechan.Payload(payload)
			if err != nil {
				t.Fatalf("payloadOf: %v", err)
			}
			if name == "nothing" {
				if len(sent) != 0 {
					t.Errorf("a command with no payload sent %s", sent)
				}
				return
			}
			var got map[string]string
			if err := json.Unmarshal(sent, &got); err != nil {
				t.Fatalf("unreadable: %s", sent)
			}
			if got["image"] != want["image"] {
				t.Errorf("arrived as %#v", got)
			}
		})
	}
}
