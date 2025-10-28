run duration faults='false' description='manual run (short)': push-daily-run
    #!/usr/bin/env bash
    
    [ "$faults" = 'true' ] && no_faults='false' || no_faults='true'
    [ "$faults" = 'true' ] && tag='daily_faults' || tag='daily_nofaults'

    curl --fail \
        --user "formance:$ANTITHESIS_PASSWORD" \
        -X POST https://formance.antithesis.com/api/v1/launch_experiment/formance-k8s -d '{
      "params": {
        "custom.duration": "{{duration}}",
        "antithesis.report.recipients": "'"$ANTITHESIS_REPORT_RECIPIENT"'",
        "antithesis.config_image": "'"antithesis-config:$tag"'",
        "antithesis.description": "{{description}}",
        "antithesis.images": "'"workload:$tag;docker.io/library/postgres:15-alpine;ghcr.io/formancehq/operator:v2.10.1;ghcr.io/formancehq/operator-utils:v2.0.14;ghcr.io/formancehq/gateway:v2.0.24;ghcr.io/formancehq/ledger-instrumented:$LEDGER_PREVIOUS_TAG;ghcr.io/formancehq/ledger-instrumented:$LEDGER_LATEST_TAG"'",
        "custom.no_faults": "'"$no_faults"'"
      }
    }'

run-12min faults='true' description ='manual run (short)' : (run '0.2' faults description)

run-1h faults='true' description='manual run (long)': (run '1' faults description)

push-daily-run:
    just config/push
    just config/push 'false'
    just workload/push
    just workload/push 'false'

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
  # kubectl port-forward $(kubectl get pod -l app.kubernetes.io/name=hdx-oss-v2 -o jsonpath="{.items[0].metadata.name}") 8081:3000 &

  wait
