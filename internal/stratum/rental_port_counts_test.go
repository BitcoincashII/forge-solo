package stratum

import "testing"

// A rented rig announces itself as the ASIC it is, not as the marketplace that rented it.
// Counting rentals from the user agent alone therefore reports zero while a paid order is
// mining -- observed live, with an "Antminer S21 XP" relayed onto the rental port and the
// dashboard showing rentals: 0. The port is the authoritative signal: this listener exists
// only for marketplace hashpower, which is exactly why its difficulty floor comes from the
// port rather than from guessing a user agent.
func TestRentalPortCountsUnrecognisedClients(t *testing.T) {
	s := NewServerForTest()
	s.config = &ServerConfig{IsRentalPort: true}
	s.clients.Store("c1", &Client{Authorized: true, DetectedMarketplace: RentalNone})

	got := s.GetRentalStats()
	if got.TotalRentals != 1 || got.OtherRentals != 1 {
		t.Errorf("an unrecognised client on the rental port counted total=%d other=%d, want 1/1",
			got.TotalRentals, got.OtherRentals)
	}
}

// The main port must NOT do this: an ordinary home miner is not a rental.
func TestMainPortDoesNotCountUnrecognisedClientsAsRentals(t *testing.T) {
	s := NewServerForTest()
	s.config = &ServerConfig{IsRentalPort: false}
	s.clients.Store("c1", &Client{Authorized: true, DetectedMarketplace: RentalNone})

	if got := s.GetRentalStats(); got.TotalRentals != 0 {
		t.Errorf("a home miner on the main port was counted as a rental (total=%d)", got.TotalRentals)
	}
}

// A recognised marketplace still attributes to that marketplace, not to "other".
func TestRecognisedMarketplaceStillWinsOnTheRentalPort(t *testing.T) {
	s := NewServerForTest()
	s.config = &ServerConfig{IsRentalPort: true}
	s.clients.Store("c1", &Client{Authorized: true, DetectedMarketplace: RentalMRR})
	s.clients.Store("c2", &Client{Authorized: true, DetectedMarketplace: RentalNiceHash})

	got := s.GetRentalStats()
	if got.MRRMiners != 1 || got.NiceHashMiners != 1 || got.OtherRentals != 0 || got.TotalRentals != 2 {
		t.Errorf("attribution lost: mrr=%d nh=%d other=%d total=%d, want 1/1/0/2",
			got.MRRMiners, got.NiceHashMiners, got.OtherRentals, got.TotalRentals)
	}
}

// An unauthorized connection is not hashpower yet, on either port.
func TestUnauthorizedClientsAreNotCounted(t *testing.T) {
	s := NewServerForTest()
	s.config = &ServerConfig{IsRentalPort: true}
	s.clients.Store("c1", &Client{Authorized: false, DetectedMarketplace: RentalNone})

	if got := s.GetRentalStats(); got.TotalRentals != 0 {
		t.Errorf("an unauthorized connection was counted (total=%d)", got.TotalRentals)
	}
}
