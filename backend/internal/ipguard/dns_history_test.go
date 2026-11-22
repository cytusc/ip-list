package ipguard

import "testing"

func TestDNSHistoryReportKeepsOnlyAppliedRecords(t *testing.T) {
	records := []dnsPlanRecord{{Name: "jp.reiiv.com", Type: "A", Desired: []string{"192.0.2.1"}}}
	candidates := []dnsCandidate{{IP: "192.0.2.1", State: "healthy"}}
	sync := dnsHistoryReport(dnsReport{Mode: "sync", Records: records, Candidates: candidates})
	if len(sync.Records) != 1 || sync.Candidates != nil {
		t.Fatalf("sync history=%+v", sync)
	}
	preview := dnsHistoryReport(dnsReport{Mode: "preview", Records: records, Candidates: candidates})
	if preview.Records != nil || preview.Candidates != nil {
		t.Fatalf("preview history=%+v", preview)
	}
}
