package api

import (
	"fmt"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/modulechan"
)

// What a module said when it was asked something and did not answer.
//
// Two shapes reach here, and they must not read the same. One is the module refusing, which is a
// decision somebody wrote down and belongs in the log the way the module's own sentence does. The
// other is the module not answering at all, which says nothing whatever about what it thinks —
// and reporting it as a refusal would put a sentence in front of a person that nobody wrote.
//
// The module's own sentence is kept verbatim rather than paraphrased. It is the only part of this
// that came from whoever knows, and "not allowed to pull x" is better than anything that could
// be composed here about it.

// moduleRefusal turns the result of asking a module something into an error, or nil.
//
// `answer` is whatever came back and `err` is whatever went wrong, because they are not
// alternatives: a module can answer with a refusal, and a module can fail to answer, and only
// one of those is a decision. Pass both rather than picking between them at the call site, since
// picking is exactly the mistake this exists to stop being made.
func moduleRefusalOf(module *models.Integration, asked string, answer []byte, err error) error {
	// A refusal in the answer: a decision somebody wrote down, kept verbatim.
	if reason, refused := modulechan.RefusalIn(answer); refused {
		return fmt.Errorf("the %s module refused: %s", module.Kind, reason)
	}

	// No refusal and no failure is an answer. Nil here and not an error, because this function
	// runs on every call and a version that could not tell success from failure would turn every
	// question the core asks into a failed one.
	if err == nil {
		return nil
	}

	// Silence, named as silence, and wrapped rather than replaced: a caller that wants to know
	// whether the module refused or merely went quiet can ask with errors.Is against ErrNoAnswer,
	// and a timeout that arrives wrapped is still recognised as the timeout it is.
	//
	// One sentence for a timeout and for a connection that was never there, because the module
	// is in the same position either way: it was not asked in a way that produced an answer.
	return fmt.Errorf("the %s module did not answer what %s is: %w", module.Kind, asked, err)
}
