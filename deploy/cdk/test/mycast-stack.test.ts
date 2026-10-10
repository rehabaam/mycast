import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import { App } from 'aws-cdk-lib';
import { Match, Template } from 'aws-cdk-lib/assertions';
import { MycastStack, MycastStackProps } from '../lib/mycast-stack';

// Fake build output, so synthesis doesn't need the Go toolchain.
function fakeDist(): string {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'mycast-dist-'));
  for (const fn of ['ingest-lambda', 'api-lambda']) {
    fs.mkdirSync(path.join(dir, fn));
    fs.writeFileSync(path.join(dir, fn, 'bootstrap'), '#!/bin/sh\n');
  }
  return dir;
}

function synth(overrides: Partial<MycastStackProps> = {}): Template {
  const app = new App();
  const stack = new MycastStack(app, 'Test', {
    env: { account: '123456789012', region: 'eu-north-1' },
    ssmPrefix: '/mycast/',
    historyDays: 7,
    fetchIntervalMin: 30,
    openMeteoEnabled: true,
    distDir: fakeDist(),
    ...overrides,
  });
  return Template.fromStack(stack);
}

test('the table is on-demand, TTL-enabled and retained', () => {
  const t = synth();
  t.hasResourceProperties('AWS::DynamoDB::Table', {
    BillingMode: 'PAY_PER_REQUEST',
    TimeToLiveSpecification: { AttributeName: 'ttl', Enabled: true },
    KeySchema: [
      { AttributeName: 'pk', KeyType: 'HASH' },
      { AttributeName: 'sk', KeyType: 'RANGE' },
    ],
  });
  t.hasResource('AWS::DynamoDB::Table', { DeletionPolicy: 'Retain', UpdateReplacePolicy: 'Retain' });
});

test('both functions are arm64 provided.al2023 with a bootstrap handler', () => {
  const t = synth();
  const fns = t.findResources('AWS::Lambda::Function', {
    Properties: { Runtime: 'provided.al2023', Architectures: ['arm64'], Handler: 'bootstrap' },
  });
  assert.equal(Object.keys(fns).length, 2);
});

test('the ingest function does not retry and is not reserved by default', () => {
  const t = synth();
  t.hasResourceProperties('AWS::Lambda::EventInvokeConfig', { MaximumRetryAttempts: 0 });
  const withReserved = t.findResources('AWS::Lambda::Function', {
    Properties: { ReservedConcurrentExecutions: Match.anyValue() },
  });
  assert.equal(Object.keys(withReserved).length, 0, 'reserving concurrency can fail a new account\'s deploy');
});

test('reserved concurrency can be switched on', () => {
  const t = synth({ ingestReservedConcurrency: 1 });
  t.hasResourceProperties('AWS::Lambda::Function', { ReservedConcurrentExecutions: 1 });
});

test('the schedule follows FETCH_INTERVAL_MIN and does not retry', () => {
  synth({ fetchIntervalMin: 10 }).hasResourceProperties('AWS::Scheduler::Schedule', {
    ScheduleExpression: 'rate(10 minutes)',
    FlexibleTimeWindow: { Mode: 'OFF' },
    Target: Match.objectLike({ Input: '{}', RetryPolicy: { MaximumRetryAttempts: 0 } }),
  });
  synth({ fetchIntervalMin: 1 }).hasResourceProperties('AWS::Scheduler::Schedule', {
    ScheduleExpression: 'rate(1 minute)',
  });
});

test('the API function has a Function URL and no function carries a secret in its environment', () => {
  const t = synth();
  t.hasResourceProperties('AWS::Lambda::Url', { AuthType: 'NONE' });

  const fns = t.findResources('AWS::Lambda::Function', {
    Properties: { Runtime: 'provided.al2023' },
  });
  for (const [id, fn] of Object.entries(fns)) {
    const env = JSON.stringify((fn as any).Properties.Environment ?? {}).toUpperCase();
    for (const forbidden of ['SECRET', 'TOKEN', 'PASSWORD']) {
      assert.ok(!env.includes(`"${forbidden}`) && !env.includes(`_${forbidden}"`), `${id} has ${forbidden} in its environment`);
    }
  }
});

const actionsOf = (s: any): string[] => ([] as string[]).concat(s.Action);

test('IAM is least-privilege: the API cannot write and reads only its own secret', () => {
  const t = synth();
  const policies = t.findResources('AWS::IAM::Policy');
  const statements = (match: string) =>
    Object.entries(policies)
      .filter(([id]) => id.includes(match))
      .flatMap(([, p]: [string, any]) => p.Properties.PolicyDocument.Statement as any[]);

  const apiActions = statements('Api').flatMap(actionsOf);
  assert.ok(!apiActions.some((a) => /PutItem|UpdateItem|DeleteItem|BatchWriteItem|\*/.test(a) && a.startsWith('dynamodb')), `API can write: ${apiActions}`);

  const apiSsm = statements('Api').filter((s) => actionsOf(s).includes('ssm:GetParameters'));
  assert.equal(apiSsm.length, 1);
  assert.ok(JSON.stringify(apiSsm[0].Resource).endsWith('parameter/mycast/api-token"}') || JSON.stringify(apiSsm[0].Resource).includes('mycast/api-token'));
  assert.ok(!JSON.stringify(apiSsm[0].Resource).includes('netatmo'), 'the API must not be able to read the Netatmo secrets');

  const ingestSsm = statements('Ingest').filter((s) => actionsOf(s).includes('ssm:GetParameters'));
  assert.ok(JSON.stringify(ingestSsm[0].Resource).includes('netatmo-client-secret'));
  assert.ok(!JSON.stringify(ingestSsm[0].Resource).includes('api-token'));
});

test('logs expire', () => {
  synth().allResourcesProperties('AWS::Logs::LogGroup', { RetentionInDays: 14 });
});

test('outputs expose the URL, table, ingest function and authorise command', () => {
  const out = synth().toJSON().Outputs;
  assert.ok(out.ApiUrl && out.TableName && out.AuthorizeCommand && out.IngestFunctionName);
});
