import type { AuthSession, RuntimeConfig } from './types';

const sessionKey = 'readest-admin-session';
let anonKey = '';

export class APIError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

export function getStoredSession(): AuthSession | null {
  try {
    const raw = sessionStorage.getItem(sessionKey);
    return raw ? (JSON.parse(raw) as AuthSession) : null;
  } catch {
    sessionStorage.removeItem(sessionKey);
    return null;
  }
}

export function storeSession(session: AuthSession | null) {
  if (session) {
    sessionStorage.setItem(sessionKey, JSON.stringify(session));
  } else {
    sessionStorage.removeItem(sessionKey);
  }
}

export async function initializeAPI() {
  const response = await fetch('/runtime-config.js?format=json', {
    credentials: 'omit',
    headers: { Accept: 'application/json' },
  });
  if (!response.ok) {
    throw new APIError(response.status, '无法读取服务器配置');
  }
  const config = (await response.json()) as RuntimeConfig;
  if (!config.supabaseAnonKey) {
    throw new APIError(500, '服务器配置不完整');
  }
  anonKey = config.supabaseAnonKey;
}

async function responseError(response: Response) {
  let message = '请求失败';
  try {
    const body = (await response.json()) as Record<string, unknown>;
    message = String(body.error ?? body.message ?? body.msg ?? message);
  } catch {
    // Keep the generic message for non-JSON failures.
  }
  return new APIError(response.status, message);
}

async function authRequest(path: string, body: unknown, accessToken?: string) {
  const response = await fetch(path, {
    method: accessToken ? 'PUT' : 'POST',
    credentials: 'omit',
    headers: {
      Accept: 'application/json',
      'Content-Type': 'application/json',
      apikey: anonKey,
      ...(accessToken ? { Authorization: `Bearer ${accessToken}` } : {}),
    },
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    throw await responseError(response);
  }
  return response;
}

export async function login(email: string, password: string) {
  const response = await authRequest('/auth/v1/token?grant_type=password', { email, password });
  const session = (await response.json()) as AuthSession;
  storeSession(session);
  return session;
}

async function refreshSession(current: AuthSession) {
  const response = await authRequest('/auth/v1/token?grant_type=refresh_token', {
    refresh_token: current.refresh_token,
  });
  const session = (await response.json()) as AuthSession;
  storeSession(session);
  return session;
}

export async function logout() {
  const session = getStoredSession();
  storeSession(null);
  if (!session) return;
  await fetch('/auth/v1/logout?scope=local', {
    method: 'POST',
    credentials: 'omit',
    headers: { apikey: anonKey, Authorization: `Bearer ${session.access_token}` },
  }).catch(() => undefined);
}

export async function adminFetch(path: string, init: RequestInit = {}, allowRefresh = true): Promise<Response> {
  const session = getStoredSession();
  if (!session) {
    throw new APIError(401, '登录已失效');
  }
  const headers = new Headers(init.headers);
  headers.set('Accept', 'application/json');
  headers.set('apikey', anonKey);
  headers.set('Authorization', `Bearer ${session.access_token}`);
  if (init.body) headers.set('Content-Type', 'application/json');
  const response = await fetch(path, { ...init, credentials: 'omit', headers });
  if (response.status === 401 && allowRefresh) {
    try {
      await refreshSession(session);
      return adminFetch(path, init, false);
    } catch {
      storeSession(null);
      throw new APIError(401, '登录已失效');
    }
  }
  if (!response.ok) {
    throw await responseError(response);
  }
  return response;
}

export async function changePassword(password: string) {
  const session = getStoredSession();
  if (!session) throw new APIError(401, '登录已失效');
  await authRequest('/auth/v1/user', { password }, session.access_token);
}
