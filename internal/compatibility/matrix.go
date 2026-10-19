package compatibility

// Source fingerprints are evidence of a specific source review, not a live-store
// certification. No upstream implementation is copied into this MIT project.
type Source struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type Version struct {
	Version           string   `json:"version"`
	Commit            string   `json:"commit"`
	ChannelInviteCode bool     `json:"channel_invite_code"`
	Sources           []Source `json:"sources"`
}

func Versions() []Version {
	versions := []Version{
		{Version: "v1.4.8", Commit: "d2e44618de03068cbbe874a23a6d7c6a249a79ec"},
		{Version: "v1.4.9", Commit: "b20da89eef9bbfbc454e028a39f1cc7b93596d37"},
		{Version: "v1.5.0", Commit: "41b86260dc9a60269526057eed8d915844ceb3a3", ChannelInviteCode: true},
	}
	for i := range versions {
		versions[i].Sources = []Source{
			{"internal/modules/channelapi/transport/http/routes.go", "61ded917819a3367247d04dddf95e2d851038387fa7c5c39472a45f661c1921f"},
			{"internal/modules/wallet/transport/http/channel_handler.go", "f4d3ea3adcb6677c0eb56293b3ae43e56aaf6e1ce739c06132b801b49c98b97b"},
			{"internal/modules/telegram/channelbot/transport/http/channel_bot_handler.go", "b0475e34ba9f3cc6883b80f17948c57b20167fb0a058e0a59a37c44ac508efb8"},
			{"internal/app/httpserver/middleware/channel_auth.go", "f6adc1b070de43195bbd128be8f72f56c27bee0136dda4cb75a69ac976061d34"},
			{"internal/platform/http/channelresponse/response.go", "cbcf4c85468b081cc9b014cb03d5c139ac2d796351c12f7dbafa9c2cc7b98c36"},
		}
		identity, response, affiliate := "2a01a25ba40a1f66b2e9c59d2cdde56867043fb0126eaa2b31d909671fcafd3b", "3a93ee73b549f485e184935b2df52a95d0ac6e83b60d4609c7c417be3573162c", "8f3a765e8596e6f6cbec022ab57d9078799218f05688c142aebb6fe6dc5e7ecd"
		if versions[i].ChannelInviteCode {
			identity, response, affiliate = "61e300374bce81bacabc1294368e7548250e871859a4d88b300e5a59f201a313", "7eda44943e90e4cbc7c057844dabff87f1203146f4bc8a8b00238afd5b0ce3ed", "318f10c1b33c5045480ab6ab1cd6a3ace7ffdefa52de7d1f3e46b1474b977b11"
		}
		versions[i].Sources = append(versions[i].Sources, Source{"internal/modules/channelapi/transport/http/channel_identity.go", identity}, Source{"internal/modules/channelapi/transport/http/response.go", response}, Source{"internal/modules/affiliate/transport/http/channel_handler.go", affiliate})
	}
	return versions
}
