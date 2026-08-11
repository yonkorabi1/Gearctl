import { FastifyInstance } from 'fastify';

export function registerHealthRoute(app: FastifyInstance): void {
  // No auth -- internal network only anyway, and this is what the docker
  // compose healthcheck polls.
  app.get('/health', async () => ({ status: 'ok' }));
}
