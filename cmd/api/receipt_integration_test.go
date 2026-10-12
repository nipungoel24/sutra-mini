package main

import (
  "bytes"
  "context"
  "encoding/json"
  "net/http"
  "net/http/httptest"
  "os"
  "sync"
  "testing"
  "time"

  "github.com/jackc/pgx/v5/pgxpool"
)

func integrationServer(t *testing.T, issued int) (*server, string) {
  t.Helper()
  dsn := os.Getenv("TEST_DATABASE_URL")
  if dsn == "" { t.Skip("TEST_DATABASE_URL is required; these tests exercise real Postgres concurrency") }
  cfg, err := pgxpool.ParseConfig(dsn); if err != nil { t.Fatal(err) }
  cfg.MaxConns = 30
  cfg.MinConns = 5
  db, err := pgxpool.NewWithConfig(context.Background(), cfg); if err != nil { t.Fatal(err) }
  t.Cleanup(db.Close)
  _, err = db.Exec(context.Background(), "TRUNCATE vouchers, jobwork_receipts, jobwork_issues, parties, skus CASCADE")
  if err != nil { t.Fatalf("database must have migrations applied: %v", err) }
  var party, sku, issue string
  db.QueryRow(context.Background(), "INSERT INTO parties(name,phone) VALUES('test worker','1') RETURNING id").Scan(&party)
  db.QueryRow(context.Background(), "INSERT INTO skus(code,name,image_url) VALUES('T-1','Test SKU','https://example.com/x') RETURNING id").Scan(&sku)
  if err:=db.QueryRow(context.Background(), "INSERT INTO jobwork_issues(party_id,sku_id,qty,issued_on) VALUES($1,$2,$3,current_date) RETURNING id",party,sku,issued).Scan(&issue);err!=nil{t.Fatal(err)}
  return &server{db:db}, issue
}
func callReceipt(t *testing.T, s *server, issue, key string) int {
	body, _ := json.Marshal(receiptRequest{IssueID: issue, Qty: 1, ReceivedOn: time.Now().UTC()})
	req := httptest.NewRequest(http.MethodPost, "/jobwork/receipts", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", key)
	rr := httptest.NewRecorder()
	s.createReceipt(rr, req)
	if rr.Code != http.StatusOK && rr.Code != http.StatusCreated && rr.Code != http.StatusConflict {
		t.Logf("callReceipt key=%s status=%d body=%s", key, rr.Code, rr.Body.String())
	}
	return rr.Code
}
func TestConcurrentReceiptsCannotOvershoot(t *testing.T){s,issue:=integrationServer(t,5);var wg sync.WaitGroup;codes:=make(chan int,10);for i:=0;i<10;i++{wg.Add(1);go func(i int){defer wg.Done();codes<-callReceipt(t,s,issue,"concurrent-"+string(rune('a'+i)))}(i)};wg.Wait();close(codes);created:=0;for code:=range codes{if code==http.StatusCreated{created++}else if code!=http.StatusConflict{t.Errorf("unexpected status %d",code)}};if created!=5{t.Fatalf("created %d receipts, want 5",created)};var total int;if err:=s.db.QueryRow(context.Background(),"SELECT COALESCE(SUM(qty),0) FROM jobwork_receipts WHERE issue_id=$1",issue).Scan(&total);err!=nil{t.Fatal(err)};if total!=5{t.Fatalf("received %d, want issued quantity 5",total)}}
func TestIdempotencyReplayCreatesNothingTwice(t *testing.T){s,issue:=integrationServer(t,5);if got:=callReceipt(t,s,issue,"same-key");got!=http.StatusCreated{t.Fatalf("first request: %d",got)};if got:=callReceipt(t,s,issue,"same-key");got!=http.StatusOK{t.Fatalf("replay: %d",got)};var receipts,vouchers int;s.db.QueryRow(context.Background(),"SELECT count(*) FROM jobwork_receipts").Scan(&receipts);s.db.QueryRow(context.Background(),"SELECT count(*) FROM vouchers").Scan(&vouchers);if receipts!=1||vouchers!=1{t.Fatalf("got %d receipts and %d vouchers; want one each",receipts,vouchers)}}
