import { FastifyRequest, FastifyReply } from 'fastify';
import * as crypto from 'crypto';

const HEADER_NAME = 'x-internal-auth';

/**
 * Service-to-service auth: platform-api sends the same static secret
 * (OLT_GATEWAY_SHARED_SECRET) in every request. This is a private,
 * internal-network-only service (no `ports:` mapping in compose -- see
 * README.md), but every request is still authenticated to prevent lateral
 * movement if another container on the same network were compromised.
 * Constant-time comparison to avoid a timing side-channel.
 */
export function verifySharedSecret(sharedSecret: string) {
  return async (request: FastifyRequest, reply: FastifyReply): Promise<void> => {
    const header = request.headers[HEADER_NAME];
    const provided = Array.isArray(header) ? header[0] : header;

    if (!provided || !constantTimeEquals(provided, sharedSecret)) {
      reply.code(401).send({ error: 'unauthorized' });
    }
  };
}

function constantTimeEquals(a: string, b: string): boolean {
  const bufA = Buffer.from(a);
  const bufB = Buffer.from(b);
  if (bufA.length !== bufB.length) {
    // Still run timingSafeEqual against same-length buffers to avoid a
    // length-based timing signal, even though the result is discarded.
    crypto.timingSafeEqual(bufA, bufA);
    return false;
  }
  return crypto.timingSafeEqual(bufA, bufB);
}
