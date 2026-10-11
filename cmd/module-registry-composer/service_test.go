package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPackageNameFromPath(t *testing.T) {
	for _, input := range []string{"vendor/package.json", "acme.lib/widget_2.json"} {
		name, ok := packageNameFromPath(input)
		if !ok || !validPackageName(name) {
			t.Errorf("%q should be a valid Composer package path, got %q, %v", input, name, ok)
		}
	}
	for _, input := range []string{"", "vendor.json", "vendor/package/extra.json", "../package.json", "vendor/../package.json", "vendor/package", "vendor/package.json/extra"} {
		if name, ok := packageNameFromPath(input); ok {
			t.Errorf("%q unexpectedly parsed as %q", input, name)
		}
	}
}

func TestDistCacheKeyIsStableAndOpaque(t *testing.T) {
	first := distCacheKey("vendor/package", "1.2.3", "https://example.test/package.zip")
	if first != distCacheKey("vendor/package", "1.2.3", "https://example.test/package.zip") {
		t.Fatal("identical package metadata generated different cache keys")
	}
	if len(first) != 64 || strings.Contains(first, "example") {
		t.Fatalf("cache key should be opaque SHA-256 hex, got %q", first)
	}
	if first == distCacheKey("vendor/package", "1.2.4", "https://example.test/package.zip") {
		t.Fatal("different versions must not share a cache key")
	}
}

func TestMergeP2DocumentsKeepsPrimaryVersionOnConflict(t *testing.T) {
	primary := []byte("{\"minified\":\"composer/2.0\",\"packages\":{\"vendor/package\":[{\"version\":\"1.0.0\",\"version_normalized\":\"1.0.0.0\",\"dist\":{\"url\":\"https://primary.test/1.zip\",\"type\":\"zip\"}},{\"version\":\"2.0.0\",\"version_normalized\":\"2.0.0.0\"}]}}")
	additional := []byte("{\"packages\":{\"vendor/package\":[{\"version\":\"2.0.0\",\"version_normalized\":\"2.0.0.0\",\"dist\":{\"url\":\"https://additional.test/2.zip\",\"type\":\"zip\"}},{\"version\":\"3.0.0\",\"version_normalized\":\"3.0.0.0\"}],\"other/package\":[{\"version\":\"1.0.0\",\"version_normalized\":\"1.0.0.0\"}]}}")

	merged, err := mergeP2Documents([][]byte{primary, additional})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Minified string "json:\"minified\""
		Packages map[string][]map[string]any "json:\"packages\""
	}
	if err := json.Unmarshal(merged, &got); err != nil {
		t.Fatal(err)
	}
	if got.Minified != "composer/2.0" {
		t.Fatalf("primary metadata fields were not preserved: %+v", got)
	}
	versions := got.Packages["vendor/package"]
	if len(versions) != 3 {
		t.Fatalf("want 3 unique versions, got %d", len(versions))
	}
	if got.Packages["other/package"] == nil {
		t.Fatal("a package present only in the additional repository was lost")
	}
	firstDist := versions[0]["dist"].(map[string]any)["url"]
	if firstDist != "https://primary.test/1.zip" {
		t.Fatalf("primary source order was not preserved: got %v", firstDist)
	}
}

func TestP2VersionCount(t *testing.T) {
	raw := []byte("{\"packages\":{\"vendor/package\":[{\"version\":\"1.0\"},{\"version\":\"2.0\"}],\"other/package\":[{\"version\":\"1.0\"}]}}")
	if got := p2VersionCount(raw); got != 3 {
		t.Fatalf("got %d versions, want 3", got)
	}
}
