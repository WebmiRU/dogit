package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ewolf/dogit/internal/models"
)

// heartbeat sends a heartbeat the way a module does, with its own instance token.
func (f *moduleFixture) heartbeat(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(http.MethodPost, "/module/heartbeat", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+f.moduleToken)

	recorder := httptest.NewRecorder()
	f.server.Routes().ServeHTTP(recorder, request)
	return recorder
}

// readStats asks for what a module last reported.
func (f *moduleFixture) readStats(t *testing.T, query string) map[string]any {
	t.Helper()

	recorder := f.asAdmin(t, http.MethodGet, "/modules/"+f.module.ID.String()+"/stats"+query, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("read stats: status %d, body %s", recorder.Code, recorder.Body.String())
	}

	var result map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return result
}

func TestModuleReportsItsStatistics(t *testing.T) {
	f := newModuleFixture(t)

	// A module that has not reported anything is not an error; the interface has
	// to be able to say so without pretending the numbers are zero.
	if stats := f.readStats(t, ""); stats["latest"] != nil {
		t.Fatalf("a module that never reported answered %v", stats["latest"])
	}

	body := `{"stats": {
		"storage_total_bytes": 1000,
		"storage_used_bytes":  750,
		"process_cpu_percent": 3.5,
		"host_load1":          0.8,
		"uptime_seconds":      3600,
		"extra": {"images": "412", "repositories": "12"}
	}}`
	if recorder := f.heartbeat(t, body); recorder.Code != http.StatusOK {
		t.Fatalf("heartbeat: status %d, body %s", recorder.Code, recorder.Body.String())
	}

	latest, _ := f.readStats(t, "")["latest"].(map[string]any)
	if latest == nil {
		t.Fatal("the reported statistics did not come back")
	}

	if used, _ := latest["storage_used_bytes"].(float64); used != 750 {
		t.Errorf("storage_used_bytes is %v, want 750", latest["storage_used_bytes"])
	}
	// A field the module did not send has to stay missing. The module is inside a
	// container and often cannot see the node's memory; a zero would read as
	// "this machine has no memory in use".
	if _, reported := latest["host_memory_total_bytes"]; reported {
		t.Error("a field the module never sent came back as a value")
	}
	if extra, _ := latest["extra"].(map[string]any); extra["images"] != "412" {
		t.Errorf("the module's own facts were lost: %v", latest["extra"])
	}
}

// A heartbeat without a body is how every module built before statistics existed
// still beats. It must keep working.
func TestHeartbeatWithoutStatisticsStillWorks(t *testing.T) {
	f := newModuleFixture(t)

	for _, body := range []string{"", "{}", "   "} {
		if recorder := f.heartbeat(t, body); recorder.Code != http.StatusOK {
			t.Fatalf("heartbeat with body %q: status %d, body %s",
				body, recorder.Code, recorder.Body.String())
		}
	}
}

// A typo in the payload is reported rather than silently dropped: otherwise a
// module would believe it was reporting something it was not.
func TestHeartbeatWithBrokenStatisticsIsRefused(t *testing.T) {
	f := newModuleFixture(t)

	recorder := f.heartbeat(t, `{"stats": {"nonsense": 1}}`)
	if recorder.Code != http.StatusBadRequest {
		t.Errorf("an unknown field: status %d, want 400", recorder.Code)
	}
	if stats := f.readStats(t, ""); stats["latest"] != nil {
		t.Error("a refused heartbeat still stored a reading")
	}
}

func TestModuleStatisticsSeries(t *testing.T) {
	f := newModuleFixture(t)

	for i := 0; i < 3; i++ {
		used := int64(100 + i*50)
		body := `{"stats": {"storage_total_bytes": 1000, "storage_used_bytes": ` +
			itoa(used) + `}}`
		if recorder := f.heartbeat(t, body); recorder.Code != http.StatusOK {
			t.Fatalf("heartbeat: status %d, body %s", recorder.Code, recorder.Body.String())
		}
	}

	from := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	to := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)

	result := f.readStats(t, "?series=1&from="+from+"&to="+to)
	series, _ := result["series"].([]any)
	if len(series) != 3 {
		t.Fatalf("got %d readings, want 3", len(series))
	}
	// Oldest first, so a graph reads left to right.
	first, _ := series[0].(map[string]any)
	last, _ := series[2].(map[string]any)
	if used, _ := first["storage_used_bytes"].(float64); used != 100 {
		t.Errorf("the first reading is %v, want 100", first["storage_used_bytes"])
	}
	if used, _ := last["storage_used_bytes"].(float64); used != 200 {
		t.Errorf("the last reading is %v, want 200", last["storage_used_bytes"])
	}
}

// A range that is not a range, or a timestamp that is not a timestamp, is a
// mistake worth reporting rather than silently returning nothing.
func TestModuleStatisticsRangeIsValidated(t *testing.T) {
	f := newModuleFixture(t)
	path := "/modules/" + f.module.ID.String() + "/stats?series=1"

	if recorder := f.asAdmin(t, http.MethodGet, path+"&from=yesterday", ""); recorder.Code != http.StatusBadRequest {
		t.Errorf("a malformed timestamp: status %d, want 400", recorder.Code)
	}
	if recorder := f.asAdmin(t, http.MethodGet,
		path+"&from="+time.Now().UTC().Format(time.RFC3339)+"&to="+
			time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), ""); recorder.Code != http.StatusBadRequest {
		t.Errorf("a backwards range: status %d, want 400", recorder.Code)
	}
}

// Storage fullness is derived, not stored: a module reports two numbers and the
// core decides what counts as full.
func TestStorageFullnessIsDerivedOnlyWhenReported(t *testing.T) {
	if _, ok := (&models.ModuleStats{}).StorageUsedFraction(); ok {
		t.Error("a reading with no storage was treated as full or empty")
	}

	total, used := int64(0), int64(0)
	if _, ok := (&models.ModuleStats{StorageTotalBytes: &total, StorageUsedBytes: &used}).StorageUsedFraction(); ok {
		t.Error("a zero total was divided by")
	}

	total, used = int64(400), int64(300)
	stats := &models.ModuleStats{StorageTotalBytes: &total, StorageUsedBytes: &used}
	fraction, ok := stats.StorageUsedFraction()
	if !ok || fraction != 0.75 {
		t.Errorf("fullness is %v (ok=%v), want 0.75", fraction, ok)
	}
}

func itoa(value int64) string { return strconv.FormatInt(value, 10) }
