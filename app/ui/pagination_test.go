package ui

import "testing"

func TestCompletedAssets(t *testing.T) {
	// Still processing (or no output path) -> nil, i.e. JSON null.
	for _, tc := range []struct{ status, writePath string }{
		{"running", "s3://bucket/out/"},
		{"queued", "s3://bucket/out/"},
		{"completed", ""},
	} {
		if got := completedAssets("wf-1", tc.status, tc.writePath); got != nil {
			t.Errorf("completedAssets(%q,%q) = %v, want nil", tc.status, tc.writePath, got)
		}
	}
	// Completed with an output path -> the full alias link set.
	got := completedAssets("wf-1", "completed", "s3://bucket/out/")
	if len(got) != len(primaryAssetDefs) {
		t.Fatalf("completed job: got %d assets, want %d", len(got), len(primaryAssetDefs))
	}
	if got[0].Name != "all.zip" || got[0].URL != "/task/wf-1/download/all.zip" {
		t.Errorf("unexpected first asset: %+v", got[0])
	}
}

func TestTaskDownloadURL(t *testing.T) {
	cases := []struct {
		uuid  string
		alias string
		want  string
	}{
		{"wf-1", "all.zip", "/task/wf-1/download/all.zip"},
		{"wf-1", "orthophoto", "/task/wf-1/download/orthophoto"},
		{"wf with space", "dsm", "/task/wf%20with%20space/download/dsm"},
	}
	for _, tc := range cases {
		if got := taskDownloadURL(tc.uuid, tc.alias); got != tc.want {
			t.Errorf("taskDownloadURL(%q,%q) = %q, want %q", tc.uuid, tc.alias, got, tc.want)
		}
	}
}

func TestParsePage(t *testing.T) {
	cases := []struct {
		raw     string
		want    int
		wantErr bool
	}{
		{"", 1, false},
		{"1", 1, false},
		{"5", 5, false},
		{"0", 0, true},
		{"-3", 0, true},
		{"abc", 0, true},
	}
	for _, tc := range cases {
		got, err := parsePage(tc.raw)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parsePage(%q): expected error, got page %d", tc.raw, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parsePage(%q): unexpected error %v", tc.raw, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parsePage(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}

func TestBuildTasksQuery(t *testing.T) {
	cases := []struct {
		name      string
		status    string
		projectID string
		limit     int
		page      int
		want      string
	}{
		{"all filters", "running", "proj-1", 25, 2, "?limit=25&page=2&projectID=proj-1&status=running"},
		{"no filters", "", "", 0, 1, "?page=1"},
		{"page clamped to 1", "", "", 25, 0, "?limit=25&page=1"},
		{"only status", "completed", "", 0, 3, "?page=3&status=completed"},
	}
	for _, tc := range cases {
		got := buildTasksQuery(tc.status, tc.projectID, tc.limit, tc.page)
		if got != tc.want {
			t.Errorf("%s: buildTasksQuery(%q,%q,%d,%d) = %q, want %q",
				tc.name, tc.status, tc.projectID, tc.limit, tc.page, got, tc.want)
		}
	}
}
