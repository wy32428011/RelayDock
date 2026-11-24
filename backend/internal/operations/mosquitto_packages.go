package operations

const reviewedMosquittoVersion = "2.1.2"

// The Noble 2.1.2 broker DEBs and these optional dependencies are pinned to
// the reviewed migration baseline in the old Mosquitto/mosquitto-ubuntu/24
// directory. The full ARM64 repository may contain more system packages;
// passing an import manifest does not give those packages permission to run.
var reviewedMosquittoDEBs = map[string]map[string]string{
	"amd64": {
		"mosquitto":          "91efd6392731e5a6eeede7eb3ed33d074fcffc03d4a559644bdbc0b5daa79373",
		"libmosquitto1":      "31803f22434e822dab3ee9555b502c3235f50ba3adb8a4f304c5a5b676cb029b",
		"libcjson1":          "43d758e214612877ec9029f2384bdbb0a6eeba6e74bb2cd3e7a60f4108947c41",
		"libmicrohttpd12t64": "4996c7f73860c9cdd3caf419ada425cf49133becf8ee52b0b08c1ee443e8272c",
	},
	"arm64": {
		"mosquitto":          "cb49aaecfcb1748a6db4026493a08c6f7c667400734e64807801524abbc39984",
		"libmosquitto1":      "a6490d9e6d1c16693ce4aa27273fb2430f1ce01fe9e2b762ce902af73dd903bf",
		"libcjson1":          "abfd20e8ff027fc6620e4c2398bafd75c66ed90094a1091b1d5b187b023103db",
		"libmicrohttpd12t64": "d3f2e1fbda47ddddb2b143d8ef3258e361aa6173e518c1d5ae1a78d0e7031988",
		"libedit2":           "cf642fec16d86517e0e715520c3a7cdaee650e7ae8a4993f82d0e1e7a5883efb",
		"libsqlite3-0":       "5a1533436569b8f10aadd9de7c83b37c05bec707c618a7aee2fea2ac58f2e8b9",
		"libbsd0":            "cdf9ff601244e64482584d9d9b98aa54a573c081ab5956227c737307196c1ce3",
		"libmd0":             "c28583825f7191cdc8b6b71c6150e2cbfc29616d6570680e5284fabc19765ea5",
	},
}
