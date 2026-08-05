export interface AuthUser {
  id: string;
  email: string;
  app_metadata?: {
    is_admin?: boolean;
  };
}

export interface AuthSession {
  access_token: string;
  refresh_token: string;
  expires_at: number;
  user: AuthUser;
}

export interface AdminUser {
  id: string;
  email: string;
  name: string;
  isAdmin: boolean;
  quotaBytes: number;
  usesDefaultQuota: boolean;
  usageBytes: number;
  fileCount: number;
  bookCount: number;
  lastSignInAt: string | null;
  createdAt: string;
}

export interface UsersResponse {
  users: AdminUser[];
  page: number;
  pageSize: number;
  total: number;
  totalPages: number;
  defaultQuotaBytes: number;
  summary: {
    totalUsers: number;
    totalFiles: number;
    totalUsageBytes: number;
  };
}

export interface RuntimeConfig {
  supabaseAnonKey: string;
}
