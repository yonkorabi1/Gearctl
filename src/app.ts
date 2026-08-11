import Fastify, { FastifyInstance } from 'fastify';
import { registerHealthRoute } from './routes/health.route';
import { registerExecuteRoute } from './routes/execute.route';

export function buildApp(opts: { sharedSecret: string }): FastifyInstance {
  const app = Fastify({
    logger: {
      // Never log request bodies by default (they carry OLT credentials in
      // `credentials.password`/`credentials.enablePassword`) -- redact
      // explicitly if request logging is ever enabled per-route.
      redact: ['req.body.credentials.password', 'req.body.credentials.enablePassword'],
    },
  });

  registerHealthRoute(app);
  registerExecuteRoute(app, opts.sharedSecret);

  return app;
}
