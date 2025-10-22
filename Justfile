run-6min: push-daily-run
    curl --fail \
        --user "formance:$ANTITHESIS_PASSWORD" \
        -X POST https://formance.antithesis.com/api/v1/launch_experiment/formance-k8s -d '{ \
          "params": { \
            "custom.duration": "0.1", \
            "antithesis.report.recipients": "'"$ANTITHESIS_SLACK_REPORT_RECIPIENT"'", \
            "antithesis.config_image": "antithesis-config:daily_run", \
            "antithesis.description": "manual run (short)", \
            "antithesis.images": "'"workload:latest;docker.io/library/postgres:15-alpine;ghcr.io/formancehq/operator:v2.10.1;ghcr.io/formancehq/operator-utils:v2.0.14;ghcr.io/formancehq/gateway:v2.0.24;ghcr.io/formancehq/ledger-instrumented:$LEDGER_PREVIOUS_TAG;ghcr.io/formancehq/ledger-instrumented:$LEDGER_LATEST_TAG"'" \
          } \
        }'

run-1h: push-daily-run
    curl --fail \
        --user "formance:$ANTITHESIS_PASSWORD" \
        -X POST https://formance.antithesis.com/api/v1/launch_experiment/formance-k8s -d '{ \
          "params": { \
            "custom.duration": "1", \
            "antithesis.report.recipients": "'"$ANTITHESIS_SLACK_REPORT_RECIPIENT"'", \
            "antithesis.config_image": "antithesis-config:daily_run", \
            "antithesis.description": "manual run (1h)", \
            "antithesis.images": "'"workload:latest;docker.io/library/postgres:15-alpine;ghcr.io/formancehq/operator:v2.10.1;ghcr.io/formancehq/operator-utils:v2.0.14;ghcr.io/formancehq/gateway:v2.0.24;ghcr.io/formancehq/ledger-instrumented:$LEDGER_PREVIOUS_TAG;ghcr.io/formancehq/ledger-instrumented:$LEDGER_LATEST_TAG"'" \
          } \
        }'

push-daily-run:
    just config/push
    just workload/push

push-instrumented-ledger:
    just image/push
