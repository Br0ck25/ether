package stats

import (
	"context"
	"github.com/braedonsaunders/ether/internal/newznab"
	"github.com/braedonsaunders/ether/internal/store"
	"time"
)

func init() {
	store.Register("stats", 2, `CREATE TABLE stats_indexer_days(indexer TEXT NOT NULL,day TEXT NOT NULL,searches INTEGER NOT NULL DEFAULT 0,successes INTEGER NOT NULL DEFAULT 0,grabs INTEGER NOT NULL DEFAULT 0,grab_success INTEGER NOT NULL DEFAULT 0,budgets INTEGER NOT NULL DEFAULT 0,latency_ms INTEGER NOT NULL DEFAULT 0,age_seconds INTEGER NOT NULL DEFAULT 0,age_samples INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(indexer,day));`)
}
func (s *Service) ObserveIndexer(e newznab.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.RecordIndexer(ctx, e); err != nil {
		s.opts.Logger.Warn("record indexer stats", "err", err)
	}
}
func (s *Service) RecordIndexer(ctx context.Context, e newznab.Event) error {
	if e.At.IsZero() {
		e.At = s.opts.Now()
	}
	search, success, grab, grabSuccess, budget, ageSamples := 0, 0, 0, 0, 0, 0
	if e.Kind == "search" {
		search = 1
		if e.Success {
			success = 1
		}
	}
	if e.Kind == "grab" {
		grab = 1
		if e.Success {
			grabSuccess = 1
		}
	}
	if e.BudgetExhausted {
		budget = 1
	}
	if e.AverageAge > 0 {
		ageSamples = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO stats_indexer_days(indexer,day,searches,successes,grabs,grab_success,budgets,latency_ms,age_seconds,age_samples) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(indexer,day) DO UPDATE SET searches=stats_indexer_days.searches+excluded.searches,successes=stats_indexer_days.successes+excluded.successes,grabs=stats_indexer_days.grabs+excluded.grabs,grab_success=stats_indexer_days.grab_success+excluded.grab_success,budgets=stats_indexer_days.budgets+excluded.budgets,latency_ms=stats_indexer_days.latency_ms+excluded.latency_ms,age_seconds=stats_indexer_days.age_seconds+excluded.age_seconds,age_samples=stats_indexer_days.age_samples+excluded.age_samples`, e.Indexer, e.At.UTC().Format("2006-01-02"), search, success, grab, grabSuccess, budget, e.Duration.Milliseconds(), int64(e.AverageAge.Seconds()), ageSamples)
	return err
}

type IndexerUsage struct {
	Indexer     string `json:"indexer"`
	Searches    int64  `json:"searches"`
	Successes   int64  `json:"successes"`
	Grabs       int64  `json:"grabs"`
	GrabSuccess int64  `json:"grabSuccess"`
	Budgets     int64  `json:"budgets"`
	LatencyMS   int64  `json:"latencyMs"`
	AgeSeconds  int64  `json:"ageSeconds"`
	AgeSamples  int64  `json:"ageSamples"`
}

func (s *Service) IndexerMonth(ctx context.Context) ([]IndexerUsage, error) {
	out := []IndexerUsage{}
	rows, err := s.db.QueryContext(ctx, `SELECT indexer,CAST(SUM(searches) AS BIGINT),CAST(SUM(successes) AS BIGINT),CAST(SUM(grabs) AS BIGINT),CAST(SUM(grab_success) AS BIGINT),CAST(SUM(budgets) AS BIGINT),CAST(SUM(latency_ms) AS BIGINT),CAST(SUM(age_seconds) AS BIGINT),CAST(SUM(age_samples) AS BIGINT) FROM stats_indexer_days WHERE day>=? GROUP BY indexer`, s.opts.Now().UTC().Format("2006-01")+"-01")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v IndexerUsage
		if err := rows.Scan(&v.Indexer, &v.Searches, &v.Successes, &v.Grabs, &v.GrabSuccess, &v.Budgets, &v.LatencyMS, &v.AgeSeconds, &v.AgeSamples); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
