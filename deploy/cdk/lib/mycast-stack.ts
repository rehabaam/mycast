import * as path from 'path';
import * as cdk from 'aws-cdk-lib';
import { Construct } from 'constructs';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as logs from 'aws-cdk-lib/aws-logs';
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
}
