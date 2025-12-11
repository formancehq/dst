run-short description='manual run (short)': push-daily-run
    curl --fail \
        --user "formance:$ANTITHESIS_PASSWORD" \
        -X POST https://formance.antithesis.com/api/v1/launch_experiment/formance-k8s -d '{ \
          "params": { \
            "custom.duration": "0.2", \
            "antithesis.report.recipients": "'"$ANTITHESIS_REPORT_RECIPIENT"'", \
            "antithesis.config_image": "antithesis-config:daily_run", \
            "antithesis.description": "{{description}}", \
            "antithesis.images": "'"workload:latest;docker.io/library/postgres:15-alpine;ghcr.io/formancehq/operator:d698973e59dd3603383a3ddb6a35c73f2727d46d;ghcr.io/formancehq/operator-utils:v3.2.0;ghcr.io/formancehq/gateway:v2.0.24;ghcr.io/formancehq/ledger-instrumented:$LEDGER_PREVIOUS_TAG;ghcr.io/formancehq/ledger-instrumented:$LEDGER_LATEST_TAG"'" \
          } \
        }'

run-long description='manual run (1h)': push-daily-run
    curl --fail \
        --user "formance:$ANTITHESIS_PASSWORD" \
        -X POST https://formance.antithesis.com/api/v1/launch_experiment/formance-k8s -d '{ \
          "params": { \
            "custom.duration": "1", \
            "antithesis.report.recipients": "'"$ANTITHESIS_REPORT_RECIPIENT"'", \
            "antithesis.config_image": "antithesis-config:daily_run", \
            "antithesis.description": "{{description}}", \
            "antithesis.images": "'"workload:latest;docker.io/library/postgres:15-alpine;ghcr.io/formancehq/operator:v2.10.1;ghcr.io/formancehq/operator-utils:v3.2.0;ghcr.io/formancehq/gateway:v2.0.24;ghcr.io/formancehq/ledger-instrumented:$LEDGER_PREVIOUS_TAG;ghcr.io/formancehq/ledger-instrumented:$LEDGER_LATEST_TAG"'" \
          } \
        }'

push-daily-run:
    just config/push
    just workload/push

push-instrumented-ledger:
    just image/push

deploy-local:
  #!/usr/bin/env bash

  set -euo pipefail
  tmpdir=$(mktemp -d)
  trap 'kill 0; rm -rf $tmpdir' EXIT SIGINT SIGTERM

  kapp delete -y -a formance-dst

  LEDGER_LATEST_TAG=$LEDGER_LATEST_TAG just workload/build
  minikube image load us-central1-docker.pkg.dev/molten-verve-216720/formance-repository/workload:latest

  LEDGER_PREVIOUS_TAG=$LEDGER_PREVIOUS_TAG just config/build-manifest "$tmpdir/resources.yaml"
  kapp deploy -y -a formance-dst -f "$tmpdir" --diff-changes
  kubectl port-forward -n stack0 $(kubectl get pod -n stack0 -l app.kubernetes.io/name=gateway -o jsonpath="{.items[0].metadata.name}") 8080:8080 &
  kubectl port-forward -n formance-systems postgres 5432:5432 &

  wait
