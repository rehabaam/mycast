#!/usr/bin/env node
import * as cdk from 'aws-cdk-lib';
import { MycastStack } from '../lib/mycast-stack';

const app = new cdk.App();

function intContext(name: string, min: number, max: number): number {
  const v = Number(app.node.tryGetContext(name));
  if (!Number.isInteger(v) || v < min || v > max) {
    throw new Error(`context "${name}" must be an integer from ${min} to ${max}, got ${app.node.tryGetContext(name)}`);
  }
  return v;
}

const reserved = app.node.tryGetContext('ingestReservedConcurrency');

new MycastStack(app, 'Mycast', {
  // Region and account come from your AWS profile; eu-north-1 (Stockholm) is
  // the nearest to Finland.
  env: { account: process.env.CDK_DEFAULT_ACCOUNT, region: process.env.CDK_DEFAULT_REGION },
  ssmPrefix: String(app.node.tryGetContext('ssmPrefix')),
  // The same bounds the Go service enforces (server/config).
  historyDays: intContext('historyDays', 2, 30),
  fetchIntervalMin: intContext('fetchIntervalMin', 1, 1440),
  openMeteoEnabled: String(app.node.tryGetContext('openMeteoEnabled')) !== 'false',
  ingestReservedConcurrency: reserved === undefined ? undefined : Number(reserved),
});
