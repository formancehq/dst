# Formance DST

Deterministic Simulation Testing of Formance systems.
Daily tests are performed via github actions.

### Testing locally

Requires a running minikube cluster.

```
LEDGER_PREVIOUS_TAG=v2.2.47 LEDGER_LATEST_TAG=v2.3.1 just deploy-local

cd workload/
GATEWAY_URL="http://127.0.0.1:8080" go run bin/cmds/first_default_ledger/main.go
```

### Triggering a run manually

```
ANTITHESIS_PASSWORD='' ANTITHESIS_REPORT_RECIPIENT="email" LEDGER_PREVIOUS_TAG=v2.2.47 LEDGER_LATEST_TAG=v2.3.1 just run-1h
```
