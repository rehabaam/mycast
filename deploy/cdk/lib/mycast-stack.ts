import * as path from 'path';
import * as cdk from 'aws-cdk-lib';
import { Construct } from 'constructs';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import * as budgets from 'aws-cdk-lib/aws-budgets';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as logs from 'aws-cdk-lib/aws-logs';
import * as sns from 'aws-cdk-lib/aws-sns';
import * as subs from 'aws-cdk-lib/aws-sns-subscriptions';
import * as scheduler from 'aws-cdk-lib/aws-scheduler';

export interface MycastStackProps extends cdk.StackProps {
  /** Parameter Store prefix holding the secrets, e.g. "/mycast/". */
  readonly ssmPrefix: string;
  /** Days of hourly history to keep (matches HISTORY_DAYS, 2-30). */
  readonly historyDays: number;
  /** Minutes between ingest runs (matches FETCH_INTERVAL_MIN, 1-1440). */
  readonly fetchIntervalMin: number;
  readonly openMeteoEnabled: boolean;

  /**
   * Reserve this much concurrency for the ingest function. Off by default:
   * a new account's total concurrency limit can be too low to reserve any
   * (AWS keeps 10 unreserved). Overlap is prevented anyway by a 60 s timeout,
   * a 30 min period and no retries.
   */
  readonly ingestReservedConcurrency?: number;

  /**
   * Reserve this much concurrency for the API function: a ceiling on how much
   * it can ever run, whatever is thrown at its public URL. Off by default for
   * the same account-limit reason as the ingest function.
   */
  readonly apiReservedConcurrency?: number;

  /**
   * Monthly spending guard. Omit `email` to deploy without one (the CDK app
   * refuses to unless you say so explicitly). AWS has no hard spending cap:
   * this is a budget that emails you early and, at `killAtPercent` of the
   * limit, stops both functions automatically.
   */
  readonly spendingGuard?: {
    /** Where the alerts go. Passed at deploy time, never committed. */
    readonly email: string;
    /** Monthly limit in USD (Budgets only supports USD). */
    readonly limitUsd: number;
    /** Share of the limit, in percent of actual spend, that stops the functions. */
    readonly killAtPercent: number;
  };

  /** Directory holding ingest-lambda/ and api-lambda/ builds (deploy/build.sh). */
  readonly distDir?: string;
}

export class MycastStack extends cdk.Stack {
  public readonly table: dynamodb.Table;
  public readonly ingestFunction: lambda.Function;
  public readonly apiFunction: lambda.Function;
  public readonly apiUrl: lambda.FunctionUrl;

  constructor(scope: Construct, id: string, props: MycastStackProps) {
    super(scope, id, props);

    const dist = props.distDir ?? path.join(__dirname, '..', '..', 'dist');
    const prefix = props.ssmPrefix.endsWith('/') ? props.ssmPrefix : `${props.ssmPrefix}/`;

    // One table, one partition: observations, the latest reading, the forecast,
    // the staleness state and the OAuth token (see server/dynamo). On-demand,
    // since the traffic is a few dozen requests an hour. Retained, because it
    // holds the Netatmo authorisation.
    this.table = new dynamodb.Table(this, 'Table', {
      partitionKey: { name: 'pk', type: dynamodb.AttributeType.STRING },
      sortKey: { name: 'sk', type: dynamodb.AttributeType.STRING },
      billingMode: dynamodb.BillingMode.PAY_PER_REQUEST,
      timeToLiveAttribute: 'ttl',
      removalPolicy: cdk.RemovalPolicy.RETAIN,
    });

    const environment: Record<string, string> = {
      MYCAST_TABLE: this.table.tableName,
      MYCAST_SSM_PREFIX: prefix,
      HISTORY_DAYS: String(props.historyDays),
      FETCH_INTERVAL_MIN: String(props.fetchIntervalMin),
      OPENMETEO_ENABLED: String(props.openMeteoEnabled),
    };

    const common = {
      runtime: lambda.Runtime.PROVIDED_AL2023,
      architecture: lambda.Architecture.ARM_64,
      handler: 'bootstrap',
      environment,
    };

    const logGroup = (name: string) =>
      new logs.LogGroup(this, `${name}Logs`, {
        retention: logs.RetentionDays.TWO_WEEKS,
        removalPolicy: cdk.RemovalPolicy.DESTROY,
      });

    this.ingestFunction = new lambda.Function(this, 'Ingest', {
      ...common,
      description: 'mycast: fetch the station reading, update the series, recompute and store the forecast',
      code: lambda.Code.fromAsset(path.join(dist, 'ingest-lambda')),
      memorySize: 256,
      timeout: cdk.Duration.seconds(60),
      // A failed run is picked up by the next one half an hour later; a retry
      // would only risk two runs rotating the Netatmo refresh token at once.
      retryAttempts: 0,
      reservedConcurrentExecutions: props.ingestReservedConcurrency,
      logGroup: logGroup('Ingest'),
    });

    this.apiFunction = new lambda.Function(this, 'Api', {
      ...common,
      description: 'mycast: serve /forecast, /current, /health and /debug from DynamoDB',
      code: lambda.Code.fromAsset(path.join(dist, 'api-lambda')),
      memorySize: 128,
      timeout: cdk.Duration.seconds(10),
      reservedConcurrentExecutions: props.apiReservedConcurrency,
      logGroup: logGroup('Api'),
    });

    // The function enforces its own bearer token (it refuses to start without
    // one), so the URL itself is open: the app can't sign requests with SigV4.
    this.apiUrl = this.apiFunction.addFunctionUrl({ authType: lambda.FunctionUrlAuthType.NONE });

    // Least privilege: the API only reads, and only its own secret.
    this.table.grantReadWriteData(this.ingestFunction);
    this.table.grantReadData(this.apiFunction);

    const parameterArn = (name: string) =>
      this.formatArn({
        service: 'ssm',
        resource: 'parameter',
        resourceName: `${prefix.replace(/^\//, '')}${name}`,
        arnFormat: cdk.ArnFormat.SLASH_RESOURCE_NAME,
      });
    this.ingestFunction.addToRolePolicy(
      new iam.PolicyStatement({
        actions: ['ssm:GetParameters'],
        resources: [parameterArn('netatmo-client-id'), parameterArn('netatmo-client-secret')],
      }),
    );
    this.apiFunction.addToRolePolicy(
      new iam.PolicyStatement({
        actions: ['ssm:GetParameters'],
        resources: [parameterArn('api-token')],
      }),
    );

    // EventBridge Scheduler: the first 14 million invocations a month are free.
    const schedulerRole = new iam.Role(this, 'SchedulerRole', {
      assumedBy: new iam.ServicePrincipal('scheduler.amazonaws.com'),
    });
    this.ingestFunction.grantInvoke(schedulerRole);

    const every =
      props.fetchIntervalMin === 1 ? 'rate(1 minute)' : `rate(${props.fetchIntervalMin} minutes)`;
    new scheduler.CfnSchedule(this, 'IngestSchedule', {
      description: 'Run the mycast ingest function',
      scheduleExpression: every,
      flexibleTimeWindow: { mode: 'OFF' },
      target: {
        arn: this.ingestFunction.functionArn,
        roleArn: schedulerRole.roleArn,
        input: '{}',
        retryPolicy: { maximumRetryAttempts: 0 },
      },
    });

    if (props.spendingGuard) {
      this.addSpendingGuard(props.spendingGuard, [this.ingestFunction, this.apiFunction]);
    }

    new cdk.CfnOutput(this, 'ApiUrl', { value: this.apiUrl.url, description: 'Base URL for the app (MyCastAPIBaseURL)' });
    new cdk.CfnOutput(this, 'TableName', { value: this.table.tableName });
    new cdk.CfnOutput(this, 'IngestFunctionName', {
      value: this.ingestFunction.functionName,
      description: 'Invoke once after authorising, instead of waiting for the schedule',
    });
    new cdk.CfnOutput(this, 'AuthorizeCommand', {
      value: `cd server && go run ./cmd/mycast-auth -table ${this.table.tableName}`,
      description: 'Run once, locally, to authorise mycast with Netatmo',
    });
  }

  /**
   * A monthly budget with early-warning emails, and an automatic kill switch.
   *
   * Budgets cannot enforce a ceiling by themselves, and their cost data lags
   * by hours, so the switch is set below the limit to leave room for that lag.
   * It sets reserved concurrency to 0 on the given functions; undoing that is
   * deliberately manual (see the ResetCommand output).
   */
  private addSpendingGuard(
    guard: NonNullable<MycastStackProps['spendingGuard']>,
    targets: lambda.Function[],
  ): void {
    const topic = new sns.Topic(this, 'SpendingAlerts', { displayName: 'mycast spending kill switch' });
    topic.addToResourcePolicy(
      new iam.PolicyStatement({
        sid: 'AllowBudgetsToPublish',
        principals: [new iam.ServicePrincipal('budgets.amazonaws.com')],
        actions: ['sns:Publish'],
        resources: [topic.topicArn],
        conditions: {
          StringEquals: { 'aws:SourceAccount': this.account },
          ArnLike: { 'aws:SourceArn': `arn:${this.partition}:budgets::${this.account}:*` },
        },
      }),
    );

    const killSwitch = new lambda.Function(this, 'KillSwitch', {
      runtime: lambda.Runtime.PYTHON_3_13,
      architecture: lambda.Architecture.ARM_64,
      handler: 'index.handler',
      description: 'mycast: stop the functions when the monthly budget is nearly spent',
      code: lambda.Code.fromAsset(path.join(__dirname, '..', 'lambda', 'kill-switch')),
      timeout: cdk.Duration.seconds(30),
      environment: { TARGETS: targets.map((f) => f.functionName).join(',') },
      logGroup: new logs.LogGroup(this, 'KillSwitchLogs', {
        retention: logs.RetentionDays.TWO_WEEKS,
        removalPolicy: cdk.RemovalPolicy.DESTROY,
      }),
    });
    // The only thing it can do, to only these functions.
    killSwitch.addToRolePolicy(
      new iam.PolicyStatement({
        actions: ['lambda:PutFunctionConcurrency'],
        resources: targets.map((f) => f.functionArn),
      }),
    );
    topic.addSubscription(new subs.LambdaSubscription(killSwitch));

    const email = { subscriptionType: 'EMAIL', address: guard.email };
    const notification = (
      type: 'ACTUAL' | 'FORECASTED',
      threshold: number,
      subscribers: { subscriptionType: string; address: string }[],
    ): budgets.CfnBudget.NotificationWithSubscribersProperty => ({
      notification: {
        notificationType: type,
        comparisonOperator: 'GREATER_THAN',
        threshold,
        thresholdType: 'PERCENTAGE',
      },
      subscribers,
    });

    new budgets.CfnBudget(this, 'MonthlyBudget', {
      budget: {
        budgetName: 'mycast-monthly',
        budgetType: 'COST',
        timeUnit: 'MONTHLY',
        budgetLimit: { amount: guard.limitUsd, unit: 'USD' },
      },
      notificationsWithSubscribers: [
        notification('ACTUAL', 50, [email]),
        notification('ACTUAL', guard.killAtPercent, [email, { subscriptionType: 'SNS', address: topic.topicArn }]),
        notification('ACTUAL', 100, [email]),
        notification('FORECASTED', 100, [email]),
      ],
    });

    new cdk.CfnOutput(this, 'ResetCommand', {
      description: 'Run after the kill switch has fired, once you have checked why',
      value: targets
        .map((f) => `aws lambda delete-function-concurrency --function-name ${f.functionName}`)
        .join(' && '),
    });
  }
}
