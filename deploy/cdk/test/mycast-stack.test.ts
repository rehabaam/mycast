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

// --- spending guard ---

const guard = { email: 'alerts@example.com', limitUsd: 50, killAtPercent: 80 };

// AWS::Budgets::Budget is missing in some regions (eu-north-1), so the budget
// is created through the Budgets API by a custom resource. Read its call back.
function budgetCall(t: Template): any {
  const res = Object.values(t.findResources('Custom::AWS')).find((r: any) => String(JSON.stringify(r.Properties.Create)).includes('createBudget')) as any;
  assert.ok(res, 'budget custom resource');
  const create = res.Properties.Create;
  const text = typeof create === 'string' ? create : (create['Fn::Join'][1] as any[]).map((x) => (typeof x === 'string' ? x : 'REF')).join('');
  return JSON.parse(text);
}

test('without a spending guard no budget or kill switch is created', () => {
  const t = synth();
  assert.equal(Object.keys(t.findResources('Custom::AWS')).length, 0);
  t.resourceCountIs('AWS::Budgets::Budget', 0);
  t.resourceCountIs('AWS::SNS::Topic', 0);
});

test('the budget is not a CloudFormation Budget resource, which eu-north-1 lacks', () => {
  const t = synth({ spendingGuard: guard });
  t.resourceCountIs('AWS::Budgets::Budget', 0);
  assert.equal(budgetCall(t).region, 'us-east-1');
});

test('the budget is $50 a month with early warnings and a kill trigger', () => {
  const t = synth({ spendingGuard: guard });
  const call = budgetCall(t);
  assert.equal(call.action, 'createBudget');
  assert.match(call.parameters.Budget.BudgetName, /^mycast-monthly-[0-9a-f]{8}$/);
  const { BudgetName: _name, ...rest } = call.parameters.Budget;
  assert.deepEqual(
    rest,
    { BudgetType: 'COST', TimeUnit: 'MONTHLY', BudgetLimit: { Amount: '50', Unit: 'USD' } },
  );
  assert.equal(call.physicalResourceId.id, call.parameters.Budget.BudgetName);

  const rows = call.parameters.NotificationsWithSubscribers.map((n: any) => ({
    type: n.Notification.NotificationType,
    at: n.Notification.Threshold,
    kinds: n.Subscribers.map((x: any) => x.SubscriptionType).sort(),
  }));
  assert.deepEqual(rows, [
    { type: 'ACTUAL', at: 50, kinds: ['EMAIL'] },
    { type: 'ACTUAL', at: 80, kinds: ['EMAIL', 'SNS'] },
    { type: 'ACTUAL', at: 100, kinds: ['EMAIL'] },
    { type: 'FORECASTED', at: 100, kinds: ['EMAIL'] },
  ]);
  // Only the 80 % alert is wired to the kill switch.
  assert.equal(rows.filter((r: any) => r.kinds.includes('SNS')).length, 1);
});

test('changing the budget settings renames it, so an update replaces rather than collides', () => {
  const a = budgetCall(synth({ spendingGuard: guard })).parameters.Budget.BudgetName;
  const b = budgetCall(synth({ spendingGuard: { ...guard, limitUsd: 60 } })).parameters.Budget.BudgetName;
  assert.notEqual(a, b);
});

test('the budget custom resource may manage only mycast budgets', () => {
  const t = synth({ spendingGuard: guard });
  const stmts = Object.entries(t.findResources('AWS::IAM::Policy'))
    .filter(([id]) => id.includes('MonthlyBudget'))
    .flatMap(([, p]: [string, any]) => p.Properties.PolicyDocument.Statement as any[])
    .filter((s) => String(s.Action).includes('budgets'));
  assert.equal(stmts.length, 1);
  assert.ok(JSON.stringify(stmts[0].Resource).includes('budget/mycast-monthly-*'));
});

test('the kill switch can change concurrency on the two functions and nothing else', () => {
  const t = synth({ spendingGuard: guard });
  const policies = Object.entries(t.findResources('AWS::IAM::Policy'))
    .filter(([id]) => id.includes('KillSwitch'))
    .flatMap(([, p]: [string, any]) => p.Properties.PolicyDocument.Statement as any[]);

  const actions = policies.flatMap((s) => ([] as string[]).concat(s.Action));
  assert.deepEqual(actions, ['lambda:PutFunctionConcurrency']);

  const resources = policies.flatMap((s) => ([] as any[]).concat(s.Resource));
  assert.equal(resources.length, 2, 'exactly the ingest and API functions');
  assert.ok(!JSON.stringify(resources).includes('*'), 'no wildcard resource');
});

test('the kill switch targets both functions and is not mixed up with them', () => {
  const t = synth({ spendingGuard: guard });
  const fns = t.findResources('AWS::Lambda::Function', { Properties: { Runtime: 'python3.13' } });
  assert.equal(Object.keys(fns).length, 1);
  const env = JSON.stringify((Object.values(fns)[0] as any).Properties.Environment.Variables.TARGETS);
  assert.ok(env.includes('Ingest') && env.includes('Api'), env);
});

test('only AWS Budgets, in this account, may publish to the alert topic', () => {
  const t = synth({ spendingGuard: guard });
  t.hasResourceProperties('AWS::SNS::TopicPolicy', {
    PolicyDocument: {
      Statement: Match.arrayWith([
        Match.objectLike({
          Principal: { Service: 'budgets.amazonaws.com' },
          Action: 'sns:Publish',
          Condition: Match.objectLike({ StringEquals: Match.anyValue(), ArnLike: Match.anyValue() }),
        }),
      ]),
    },
  });
});

test('the email address appears only in the budget call', () => {
  const t = synth({ spendingGuard: guard });
  const res = t.toJSON().Resources;
  const holders = Object.entries(res).filter(([, r]) => JSON.stringify(r).includes('alerts@example.com'));
  assert.equal(holders.length, 1);
  assert.equal((holders[0][1] as any).Type, 'Custom::AWS');
});

test('the reset command and an API concurrency ceiling are available', () => {
  const t = synth({ spendingGuard: guard, apiReservedConcurrency: 3 });
  assert.ok(t.toJSON().Outputs.ResetCommand);
  t.hasResourceProperties('AWS::Lambda::Function', { ReservedConcurrentExecutions: 3 });
});
