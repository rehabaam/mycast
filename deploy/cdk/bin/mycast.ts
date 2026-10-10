#!/usr/bin/env node
import * as cdk from 'aws-cdk-lib';
import { MycastStack, MycastStackProps } from '../lib/mycast-stack';

const app = new cdk.App();

function intContext(name: string, min: number, max: number): number {
  const v = Number(app.node.tryGetContext(name));
  if (!Number.isInteger(v) || v < min || v > max) {
    throw new Error(`context "${name}" must be an integer from ${min} to ${max}, got ${app.node.tryGetContext(name)}`);
  }
  return v;
}

const optionalInt = (name: string): number | undefined => {
  const v = app.node.tryGetContext(name);
  return v === undefined ? undefined : Number(v);
};

// Fail closed: a deploy without a spending guard has to be asked for by name,
// so a missing secret can't quietly leave the account unprotected.
const budgetEmail = String(app.node.tryGetContext('budgetEmail') ?? '').trim();
if (budgetEmail === '') {
  throw new Error(
    'context "budgetEmail" is required: pass -c budgetEmail=you@example.com, ' +
      '(in CI, set the BUDGET_EMAIL repository secret), or -c budgetEmail=none to deploy with no spending guard',
  );
}
let spendingGuard: MycastStackProps['spendingGuard'];
if (budgetEmail.toLowerCase() !== 'none') {
  if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(budgetEmail)) {
    throw new Error('context "budgetEmail" does not look like an email address');
  }
  spendingGuard = {
    email: budgetEmail,
    limitUsd: intContext('budgetLimitUsd', 1, 100000),
    killAtPercent: intContext('killAtPercent', 10, 100),
  };
}

new MycastStack(app, 'Mycast', {
  // Region and account come from your AWS profile; eu-north-1 (Stockholm) is
  // the nearest to Finland.
  env: { account: process.env.CDK_DEFAULT_ACCOUNT, region: process.env.CDK_DEFAULT_REGION },
  ssmPrefix: String(app.node.tryGetContext('ssmPrefix')),
  // The same bounds the Go service enforces (server/config).
  historyDays: intContext('historyDays', 2, 30),
  fetchIntervalMin: intContext('fetchIntervalMin', 1, 1440),
  openMeteoEnabled: String(app.node.tryGetContext('openMeteoEnabled')) !== 'false',
  ingestReservedConcurrency: optionalInt('ingestReservedConcurrency'),
  apiReservedConcurrency: optionalInt('apiReservedConcurrency'),
  spendingGuard,
});
