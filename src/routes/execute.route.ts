import { FastifyInstance } from 'fastify';
import { ExecuteRequestSchema, ExecuteResponse, OltCliError } from '../types';
import { resolveDriver } from '../drivers/driver-registry';
import { isAllowedOltTarget } from '../security/target-validator';
import { verifySharedSecret } from '../auth/shared-secret.middleware';

export function registerExecuteRoute(app: FastifyInstance, sharedSecret: string): void {
  app.post(
    '/v1/execute',
    { preHandler: verifySharedSecret(sharedSecret) },
    async (request, reply) => {
      const parsed = ExecuteRequestSchema.safeParse(request.body);
      if (!parsed.success) {
        reply.code(400).send({
          status: 'failed',
          results: [],
          error: { type: 'validation', message: parsed.error.message },
          connectedAt: null,
          totalDurationMs: 0,
        } satisfies ExecuteResponse);
        return;
      }
      const body = parsed.data;

      if (!isAllowedOltTarget(body.host)) {
        reply.code(400).send({
          status: 'failed',
          results: [],
          error: { type: 'validation', message: `Target host "${body.host}" is not allowed` },
          connectedAt: null,
          totalDurationMs: 0,
        } satisfies ExecuteResponse);
        return;
      }

      const startedAt = Date.now();
      const driver = resolveDriver(body.vendor);

      try {
        await driver.connect({
          host: body.host,
          port: body.port,
          protocol: body.protocol,
          username: body.credentials.username,
          password: body.credentials.password,
          enablePassword: body.credentials.enablePassword,
          connectTimeoutMs: body.connectTimeoutMs,
          perCommandTimeoutMs: body.perCommandTimeoutMs,
          disablePagingCommand: body.disablePagingCommand,
        });

        const connectedAt = new Date().toISOString();
        const results = await driver.runCommands(body.commands);
        const anyFailed = results.some((r) => r.error);
        const allFailed = results.every((r) => r.error);

        const response: ExecuteResponse = {
          status: allFailed ? 'failed' : anyFailed ? 'partial' : 'success',
          results,
          error: null,
          connectedAt,
          totalDurationMs: Date.now() - startedAt,
        };
        reply.code(200).send(response);
      } catch (err) {
        const oltErr =
          err instanceof OltCliError ? err : new OltCliError('connection', (err as Error).message);
        const response: ExecuteResponse = {
          status: 'failed',
          results: [],
          error: { type: oltErr.type, message: oltErr.message },
          connectedAt: null,
          totalDurationMs: Date.now() - startedAt,
        };
        reply.code(200).send(response);
      } finally {
        await driver.disconnect().catch(() => {
          // Best-effort cleanup -- a failure to tear down cleanly shouldn't
          // change the response already sent to the caller.
        });
      }
    },
  );
}
