// Command singleton_driver_model runs a model-based conformance test: it drives
// the ledger itself and checks every response against an in-memory model that
// predicts the set of legal outcomes.
//
// It lives in its own Antithesis test template (model), separate from the rest
// of the suite (main). Antithesis selects exactly one template per execution
// history, so the model driver never runs alongside another driver and owns the
// whole timeline. Each bin/cmds/<template>/ directory becomes a test template,
// so keeping this command under bin/cmds/model/ is what keeps it isolated.
package main

import "log"

func main() {
	log.Println("composer: singleton_driver_model")
}
