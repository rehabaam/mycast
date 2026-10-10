"""Spending kill switch for mycast.

AWS Budgets publishes to an SNS topic when spend crosses the configured share
of the monthly limit; this function then sets reserved concurrency to 0 on the
mycast functions, which stops them from running (and from costing anything).

Resetting is deliberately manual, once you have looked at why it fired:

    aws lambda delete-function-concurrency --function-name <name>

Invoke it by hand with {"dryRun": true} to see what it would do.
"""
import json
import os

import boto3

_lambda = boto3.client("lambda")


def handler(event, context):
    targets = [name for name in os.environ.get("TARGETS", "").split(",") if name]
    if not targets:
        raise RuntimeError("TARGETS is empty: nothing to stop")

    dry_run = isinstance(event, dict) and bool(event.get("dryRun"))
    results, failures = [], []

    for name in targets:
        if dry_run:
            results.append({"function": name, "action": "would set reserved concurrency to 0"})
            continue
        try:
            _lambda.put_function_concurrency(FunctionName=name, ReservedConcurrentExecutions=0)
            results.append({"function": name, "action": "reserved concurrency set to 0"})
        except Exception as exc:  # keep going: stop as many as possible
            failures.append({"function": name, "error": type(exc).__name__})

    print(json.dumps({"killSwitch": results, "failures": failures, "dryRun": dry_run}))

    if failures:
        # Fail the invocation so SNS retries it and the failure is visible.
        raise RuntimeError(f"could not stop: {[f['function'] for f in failures]}")
    return results
