package api

// Whether an image a place has deployed is still in the registry that place pulls from.
//
// The catalogue the page draws is this core's deploy module's memory: what was deployed
// here, with which digest. That memory is a record of the past and says nothing about the
// present, and the two come apart in the ordinary way — somebody cleans up a registry, a
// repository is deleted, an image is re-pushed under a new name — at which point the page
// offers to put back a version that cannot be fetched, and the finding out is a rollback
// that changes nothing and a workload stuck on whatever the failed rollout left behind.
//
// So the page asks, once per page of images, and the question is put to the registry rather
// than to anything that remembers. Three answers, because they are not two: an image that is
// there, an image the registry says it does not have, and a registry that did not answer.
// Only the second is acted on. A timeout must not make every rollback on an instance dead,
// and a registry that is asleep has not said that anything is gone.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ewolf/dogit/internal/store"
)

// availabilityLimit is how many images one request may ask about.
//
// A page of the catalogue is ten and this is generous by a factor of five. The limit is
// here because the request costs the registry something — a manifest lookup each — and a
// page that asked about everything would be a page asking a registry for a hundred thousand
// lookups on somebody else's behalf.
const availabilityLimit = 50

// imageAvailability is the answer about one image.
type imageAvailability struct {
	// Image is the name as the catalogue has it, so a page can match an answer to a row
	// without having to reconstruct what it asked.
	Image string `json:"image"`
	// State is present, missing, or unknown.
	State string `json:"state"`
}

// handleProjectDeployImagesAvailability asks the place's registry about a page of images.
//
// The credential is resolved here and not by the module, for the same reason a deployment's
// is: the core holds the list of registries and the tokens, and a module that could name any
// registry and be handed its password would make every registry on the instance its
// business. It is sent for one request, to the module that asked for it, and is not written
// anywhere.
//
// The images are rewritten to the place's registry before they are asked about, exactly as a
// deployment rewrites them. The image the catalogue holds is the one that was pushed, which
// may be at an address this place no longer pulls from, and asking a registry about an image
// under a name it never served would report as missing an image that is there under the name
// a pull would use.
func (s *Server) handleProjectDeployImagesAvailability(w http.ResponseWriter, r *http.Request) {
	project, _, err := s.projectWithAccess(r, store.ActionReadCI)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	module, err := s.deployModuleFor(r, project)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if module == nil {
		s.writeJSON(w, r, http.StatusOK, map[string]any{
			"reason": "no_deploy_module", "images": []any{},
		})
		return
	}

	var request struct {
		Cluster   string   `json:"cluster"`
		Namespace string   `json:"namespace"`
		Images    []string `json:"images"`
	}
	if err := decodeJSON(r, &request); err != nil {
		s.writeError(w, r, err)
		return
	}
	request.Cluster = strings.TrimSpace(request.Cluster)
	if request.Cluster == "" {
		s.writeError(w, r, errBadRequest(
			"say which place these images are in: a registry is asked about one place's pulls"))
		return
	}
	if len(request.Images) == 0 {
		s.writeJSON(w, r, http.StatusOK, map[string]any{"images": []any{}})
		return
	}
	if len(request.Images) > availabilityLimit {
		s.writeError(w, r, errBadRequestf(
			"only %d images can be asked about at once, and %d were sent",
			availabilityLimit, len(request.Images)))
		return
	}

	// The same decision a deployment makes, from the same place: a place that has named no
	// registry, or named one this instance does not know, cannot be asked either — and the
	// sentence is the one the deployment would be refused with, because it is the same
	// thing wrong.
	pull, err := s.placePullFrom(r.Context(), project, module, request.Cluster, "", nil)
	if err != nil {
		if sentence, refused := registryRefusal(err); refused {
			s.writeError(w, r, errBadRequest(sentence))
			return
		}
		s.writeError(w, r, err)
		return
	}

	// What is asked about, and which row on the page each answer belongs to. A row that
	// cannot be asked about is still answered — as not known — because a page of images
	// where one row is blank reads as a row that was never loaded.
	type question struct {
		image, path, reference string
		asked                  bool
	}
	questions := make([]question, 0, len(request.Images))
	asked := make([]map[string]string, 0, len(request.Images))
	for _, image := range request.Images {
		path, reference := imageReferenceAt(image, pull.Address)
		one := question{
			image: strings.TrimSpace(image), path: path, reference: reference,
			asked: path != "" && reference != "",
		}
		questions = append(questions, one)
		if one.asked {
			asked = append(asked, map[string]string{"path": path, "reference": reference})
		}
	}

	if len(asked) == 0 {
		// Nothing on this page can be asked about. Asking the registry anyway would spend
		// a request to be told there is nothing to say, and the page is answered by what
		// this already knows: not known.
		out := make([]imageAvailability, 0, len(questions))
		for _, one := range questions {
			out = append(out, imageAvailability{Image: one.image, State: "unknown"})
		}
		s.writeJSON(w, r, http.StatusOK, map[string]any{"images": out})
		return
	}

	body, err := json.Marshal(map[string]any{
		"registry": map[string]any{
			"address":      pull.Address,
			"username":     pull.Username,
			"token":        pull.Token,
			"insecure_tls": pull.InsecureTLS,
		},
		"images": asked,
	})
	if err != nil {
		s.writeError(w, r, fmt.Errorf("could not describe the question: %w", err))
		return
	}
	answer, err := s.callDeployModule(r.Context(), module, http.MethodPost,
		"/images-availability", body)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	// The module answers about what it was asked about, in order, and this says which of
	// those answers is which image on the page. An answer that does not line up — a module
	// that skipped one, or reordered them — leaves that image unasked rather than giving it
	// somebody else's answer, and an image nobody has asked about is shown as such.
	var states struct {
		Images []struct {
			Path      string `json:"path"`
			Reference string `json:"reference"`
			State     string `json:"state"`
		} `json:"images"`
	}
	if err := json.Unmarshal(answer, &states); err != nil {
		s.writeError(w, r, fmt.Errorf("could not read what the %s module said: %w", module.Kind, err))
		return
	}

	out := make([]imageAvailability, 0, len(questions))
	next := 0
	for _, one := range questions {
		state := "unknown"
		if one.asked && next < len(states.Images) && states.Images[next].State != "" {
			state = states.Images[next].State
		}
		if one.asked {
			next++
		}
		out = append(out, imageAvailability{Image: one.image, State: state})
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"images": out})
}

// imageReferenceAt is what to ask a registry about an image, at one address: the repository
// without its host, and the digest or the tag it is wanted by.
//
// The repository is moved to the place's registry first, because that is the name a pull
// would use: an image the catalogue still records at the address it was pushed to is at this
// address only if this place has not pulled from somewhere else since.
//
// A name with neither a digest nor a tag answers empty, because nothing can be fetched by it
// and a registry asked about it can only say "no" — which would be read as the image being
// gone, when what has happened is that nobody ever said which image it was.
func imageReferenceAt(image, address string) (path, reference string) {
	image = strings.TrimSpace(image)
	if at := strings.LastIndex(image, "@"); at > 0 {
		reference = strings.TrimSpace(image[at+1:])
		image = image[:at]
	}
	repository, tag := splitImage(image)
	if reference == "" {
		reference = strings.TrimSpace(tag)
	}
	// Nothing to fetch by, so nothing to ask about. A digest that is only its own
	// separator is not a digest.
	if repository == "" || reference == "" || strings.HasSuffix(reference, ":") {
		return "", ""
	}

	// A record written with a scheme is a record that treats an image name as an address,
	// and taking the host off it here is the difference between asking about "team/app"
	// and asking about a repository called "harbor.example.com".
	repository = registryAddressOf(repository)
	if strings.Contains(repository, "://") {
		repository = ""
	}
	_, path, found := strings.Cut(imageAtRegistry(repository, address), "/")
	if !found || strings.TrimSpace(path) == "" {
		return "", ""
	}
	return path, reference
}
