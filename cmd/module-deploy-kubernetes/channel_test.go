package main

import (
	"encoding/json"
	"testing"

	"github.com/ewolf/dogit/internal/modulechan"
)

// The deploy module's half of the channel.
//
// The interesting property is that both transports reach the same function. A question asked on
// the socket and the same question asked over HTTP must produce the same answer, and the only way
// to hold that is for there to be one implementation — so these check that the two paths land in
// the same place and that a refusal looks the same whichever way it arrived.

// answerable is the gate a kind passes before this module will act on it.
//
// The kinds that are not on the list are the whole of what a deploy must not do here. `deploy` and
// `revert` narrate themselves line by line and stay on HTTP; if either ever appears on the channel
// it must be ignored rather than half-answered, because a module that acknowledges a deploy it is
// not going to perform has told the core something untrue.
func TestOnlyTheQuestionsAreAnsweredOnTheChannel(t *testing.T) {
	answered := []string{
		modulechan.DeployImages,
		modulechan.DeployCurrent,
		modulechan.DeployDeployments,
		modulechan.DeployImagesAvailability,
		modulechan.DeployTestCluster,
	}
	for _, kind := range answered {
		if !answerable(kind) {
			t.Errorf("this module does not answer %q on the channel, and the core asks it", kind)
		}
	}

	// The ones that must be refused by omission, including the two that would be dangerous.
	for _, kind := range []string{"deploy", "revert", "deploy.stream", "", "whatever.a.newer.core.sends"} {
		if answerable(kind) {
			t.Errorf("this module would answer %q on the channel", kind)
		}
	}
}

// The names are a contract between two processes. A kind renamed on one side and not the other is a
// question silently never asked.
func TestTheQuestionKindsAreTheOnesTheCoreSends(t *testing.T) {
	for kind, want := range map[string]string{
		"deploy.images":              modulechan.DeployImages,
		"deploy.current":             modulechan.DeployCurrent,
		"deploy.deployments":         modulechan.DeployDeployments,
		"deploy.images_availability": modulechan.DeployImagesAvailability,
		"deploy.clusters.test":       modulechan.DeployTestCluster,
	} {
		if kind != want {
			t.Errorf("a kind is %q on this side and %q on the core's", want, kind)
		}
	}
}

// A refusal is recognised by what it says rather than by a status code, because a channel message
// has no status. Getting this wrong means a refusal reaches a page as an answer the page cannot
// read, which is worse than an error.
func TestARefusalIsRecognisedByItsShape(t *testing.T) {
	refused := refusedOutcome(403, "not allowed to deploy to production")
	if got := refused.refusal(); got != "not allowed to deploy to production" {
		t.Errorf("a refusal reads as %q", got)
	}

	answered := outcome{status: 200, body: map[string]any{"images": []any{}, "total": 0}}
	if got := answered.refusal(); got != "" {
		t.Errorf("an answer was read as a refusal: %q", got)
	}
}

// An answer that carries an `error` key with nothing in it is an answer, not a refusal. Anything
// else would turn every empty-but-successful answer into an error page.
func TestAnErrorKeyWithNoSentenceIsNotARefusal(t *testing.T) {
	for _, body := range []any{
		map[string]any{"error": map[string]any{}},
		map[string]any{"error": map[string]any{"message": ""}},
		map[string]any{"error": "a string"},
		map[string]any{"error": nil},
		"not an object",
		nil,
	} {
		if got := (outcome{status: 200, body: body}).refusal(); got != "" {
			t.Errorf("%#v was read as a refusal: %q", body, got)
		}
	}
}

// A question arrives with its parameters and, for two of the five, a body. Both have to survive the
// trip, because a question that loses its project is a question about somebody else's deploy.
func TestAQuestionCarriesItsParametersAndItsBody(t *testing.T) {
	payload, err := json.Marshal(question{
		Query: map[string]string{"project": "home-store/www", "cluster": "production-eu"},
		Body:  json.RawMessage(`{"registry":{"address":"registry.f220.ru"}}`),
	})
	if err != nil {
		t.Fatalf("encode the question: %v", err)
	}

	var arrived question
	if err := json.Unmarshal(payload, &arrived); err != nil {
		t.Fatalf("decode the question: %v", err)
	}

	values := arrived.values()
	if values.Get("project") != "home-store/www" {
		t.Errorf("the project arrived as %q", values.Get("project"))
	}
	if values.Get("cluster") != "production-eu" {
		t.Errorf("the cluster arrived as %q", values.Get("cluster"))
	}
	if string(arrived.Body) != `{"registry":{"address":"registry.f220.ru"}}` {
		t.Errorf("the body arrived as %s", arrived.Body)
	}
}

// A question with nothing in it must not become a nil map that a caller reading it will panic on.
func TestAQuestionWithNoParametersIsStillReadable(t *testing.T) {
	values := question{}.values()
	if values == nil {
		t.Fatal("an empty question became nil")
	}
	if len(values) != 0 {
		t.Errorf("an empty question carried %d parameters", len(values))
	}
}
