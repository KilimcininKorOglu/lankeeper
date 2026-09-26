package services

import (
	"strings"
	"testing"
)

// Retention deletes the oldest objects first, so an object whose time
// cannot be read must stop the listing rather than rank as oldest.
func TestS3ListingRefusesAnUnreadableTimestamp(t *testing.T) {
	body := `<ListBucketResult>
  <Contents><Key>a.tar.gz</Key><LastModified>2026-09-01T03:00:00.000Z</LastModified><Size>1</Size></Contents>
  <Contents><Key>b.tar.gz</Key><LastModified>Tue, 01 Sep 2026 03:00:00 GMT</LastModified><Size>1</Size></Contents>
</ListBucketResult>`
	if _, err := decodeListing(strings.NewReader(body)); err == nil || !strings.Contains(err.Error(), "b.tar.gz") {
		t.Fatalf("decodeListing: err = %v, want a refusal naming b.tar.gz", err)
	}
}
