"""Tests for lambda/kill-switch/index.py with boto3 stubbed out."""
import importlib.util
import json
import os
import sys
import types
import unittest
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))
MODULE = os.path.join(HERE, "..", "lambda", "kill-switch", "index.py")


def load(client):
    """Import the function with boto3.client() returning `client`."""
    fake = types.ModuleType("boto3")
    fake.client = lambda service: client
    with mock.patch.dict(sys.modules, {"boto3": fake}):
        spec = importlib.util.spec_from_file_location("kill_switch_index", MODULE)
        mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)
    return mod


class KillSwitch(unittest.TestCase):
    def setUp(self):
        self.client = mock.Mock()
        self.mod = load(self.client)
        patcher = mock.patch.dict(os.environ, {"TARGETS": "mycast-ingest,mycast-api"})
        patcher.start()
        self.addCleanup(patcher.stop)

    def test_stops_every_target(self):
        out = self.mod.handler({"Records": [{"Sns": {"Message": "budget exceeded"}}]}, None)

        calls = [c.kwargs for c in self.client.put_function_concurrency.call_args_list]
        self.assertEqual(
            calls,
            [
                {"FunctionName": "mycast-ingest", "ReservedConcurrentExecutions": 0},
                {"FunctionName": "mycast-api", "ReservedConcurrentExecutions": 0},
            ],
        )
        self.assertEqual(len(out), 2)

    def test_dry_run_changes_nothing(self):
        out = self.mod.handler({"dryRun": True}, None)

        self.client.put_function_concurrency.assert_not_called()
        self.assertTrue(all("would" in r["action"] for r in out))

    def test_one_failure_does_not_stop_the_others_and_fails_the_run(self):
        def flaky(FunctionName, ReservedConcurrentExecutions):
            if FunctionName == "mycast-ingest":
                raise RuntimeError("AccessDenied: secret detail")

        self.client.put_function_concurrency.side_effect = flaky

        with self.assertRaises(RuntimeError) as ctx:
            self.mod.handler({}, None)

        names = [c.kwargs["FunctionName"] for c in self.client.put_function_concurrency.call_args_list]
        self.assertEqual(names, ["mycast-ingest", "mycast-api"], "the second function must still be attempted")
        self.assertIn("mycast-ingest", str(ctx.exception))
        self.assertNotIn("secret detail", str(ctx.exception), "error text from AWS must not leak into the failure")

    def test_refuses_to_run_with_nothing_to_stop(self):
        with mock.patch.dict(os.environ, {"TARGETS": ""}):
            with self.assertRaises(RuntimeError):
                self.mod.handler({}, None)

    def test_ignores_what_the_event_contains(self):
        # Only the topic's publishers (Budgets) can reach this; the payload is
        # not trusted or used to choose what to stop.
        self.mod.handler({"dryRun": False, "TARGETS": "someone-elses-function"}, None)

        names = [c.kwargs["FunctionName"] for c in self.client.put_function_concurrency.call_args_list]
        self.assertEqual(names, ["mycast-ingest", "mycast-api"])


if __name__ == "__main__":
    unittest.main()
