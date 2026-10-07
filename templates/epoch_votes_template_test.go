package templates

import (
	"bytes"
	"strings"
	"testing"
	"text/template"

	"github.com/ethpandaops/dora/types/models"
)

func TestEpochTemplatesGateRoundNetworkVotes(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		epochRow := &models.EpochsPageDataEpoch{EpochVotesUnavailable: unavailable, TargetVoteParticipation: 23.12, HeadVoteParticipation: 98.76, TotalVoteParticipation: 99.87}
		indexRow := &models.IndexPageDataEpochs{EpochVotesUnavailable: unavailable, VoteParticipation: 23.12}
		for _, tt := range []struct {
			file     string
			name     string
			data     any
			wantLink string
		}{
			{"epoch/epoch.html", "page", &models.EpochPageData{EpochVotesUnavailable: unavailable, TargetVoteParticipation: 23.12, HeadVoteParticipation: 98.76, FinalityThreshold: 66.67}, "View round participation"},
			{"epochs/epochs.html", "page", &models.EpochsPageData{EpochCount: 1, Epochs: []*models.EpochsPageDataEpoch{epochRow}}, "See rounds"},
			{"index/recentEpochs.html", "recentEpochs", &models.IndexPageData{RecentEpochCount: 1, RecentEpochs: []*models.IndexPageDataEpochs{indexRow}}, "See rounds"},
		} {
			t.Run(tt.file, func(t *testing.T) {
				body, err := Files.ReadFile(tt.file)
				if err != nil {
					t.Fatal(err)
				}
				tmpl, err := template.New("t").Funcs(template.FuncMap(templateFuncs)).Parse(string(body))
				if err != nil {
					t.Fatal(err)
				}
				// The empty epoch-list panel references this SVG even when unused.
				if _, err := tmpl.New("professor_svg").Parse(""); err != nil {
					t.Fatal(err)
				}
				var out bytes.Buffer
				if err := tmpl.ExecuteTemplate(&out, tt.name, tt.data); err != nil {
					t.Fatal(err)
				}
				result := out.String()
				if strings.Contains(result, "23.12%") == unavailable {
					t.Errorf("target percentage visibility incorrect (unavailable = %v)", unavailable)
				}
				if tt.file != "index/recentEpochs.html" && strings.Contains(result, "98.76%") == unavailable {
					t.Errorf("head percentage visibility incorrect (unavailable = %v)", unavailable)
				}
				if unavailable && (!strings.Contains(result, `href="/#recent-rounds"`) || !strings.Contains(result, tt.wantLink)) {
					t.Error("unavailable metrics need a round participation link")
				}
				if tt.file == "epoch/epoch.html" && strings.Contains(result, "cannot be justified") == unavailable {
					t.Errorf("epoch finality warning visibility incorrect (unavailable = %v)", unavailable)
				}
			})
		}
	}
}
