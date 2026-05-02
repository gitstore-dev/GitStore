import React, { createContext, useContext, useState, useEffect, useCallback, type ReactNode } from 'react';
import { logger } from './logger';

interface User {
  username: string;
  isAdmin: boolean;
}

interface AuthState {
  user: User | null;
  token: string | null;
  isLoading: boolean;
  isAuthenticated: boolean;
}

interface AuthContextValue extends AuthState {
  login: (username: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
  refreshToken: () => Promise<void>;
  checkAuth: () => void;
}

const AuthContext = createContext<AuthContextValue | undefined>(undefined);

interface AuthProviderProps {
  children: ReactNode;
}

const LOGIN_ENDPOINT = import.meta.env.PUBLIC_GITSTORE_LOGIN_ENDPOINT || 'http://localhost:4000/api/login';

export function AuthProvider({ children }: Readonly<AuthProviderProps>) {
  const [state, setState] = useState<AuthState>({
    user: null,
    token: null,
    isLoading: true,
    isAuthenticated: false,
  });

  // Check if user is authenticated on mount
  useEffect(() => {
    checkAuth();
  }, []);

  // Set up token refresh timer
  useEffect(() => {
    if (!state.token) return;

    // Refresh token 5 minutes before expiry
    const expiresAt = localStorage.getItem('auth_expires_at');
    if (expiresAt) {
      const expiryTime = new Date(expiresAt).getTime();
      const now = Date.now();
      const timeUntilRefresh = expiryTime - now - 5 * 60 * 1000; // 5 minutes before expiry

      if (timeUntilRefresh > 0) {
        const refreshTimer = setTimeout(() => {
          refreshToken();
        }, timeUntilRefresh);

        return () => clearTimeout(refreshTimer);
      } else {
        // Token already expired or about to expire, refresh now
        refreshToken();
      }
    }
  }, [state.token]);

  const checkAuth = useCallback(() => {
    const token = localStorage.getItem('auth_token');
    const userStr = localStorage.getItem('auth_user');
    const expiresAt = localStorage.getItem('auth_expires_at');

    if (token && userStr && expiresAt) {
      // Check if token is expired
      const expiryTime = new Date(expiresAt).getTime();
      const now = Date.now();

      if (expiryTime > now) {
        const user = JSON.parse(userStr);
        setState({
          user,
          token,
          isLoading: false,
          isAuthenticated: true,
        });
        logger.debug('User authenticated from localStorage', { username: user.username });
      } else {
        // Token expired, clear storage
        logger.debug('Token expired, clearing localStorage');
        localStorage.removeItem('auth_token');
        localStorage.removeItem('auth_user');
        localStorage.removeItem('auth_expires_at');
        setState({
          user: null,
          token: null,
          isLoading: false,
          isAuthenticated: false,
        });
      }
    } else {
      setState({
        user: null,
        token: null,
        isLoading: false,
        isAuthenticated: false,
      });
    }
  }, []);

  const login = useCallback(async (username: string, password: string) => {
    logger.debug('Attempting login', { username });

    try {
      const credentials = btoa(`${username}:${password}`);
      const response = await fetch(LOGIN_ENDPOINT, {
        method: 'POST',
        headers: {
          'Authorization': `Basic ${credentials}`,
          'Content-Type': 'application/json',
        },
      });

      if (!response.ok) {
        const errorText = await response.text();
        logger.error('Login failed', { status: response.status, error: errorText });
        throw new Error(response.status === 401 ? 'Invalid username or password' : 'Login failed');
      }

      const result = await response.json();

      if (!result.token) {
        logger.error('Invalid login response', { result });
        throw new Error('Invalid response from server');
      }

      // Store token and user info
      localStorage.setItem('auth_token', result.token);
      localStorage.setItem('auth_expires_at', result.expiresAt);
      localStorage.setItem('auth_user', JSON.stringify({
        username: result.username,
        isAdmin: result.isAdmin,
      }));

      setState({
        user: {
          username: result.username,
          isAdmin: result.isAdmin,
        },
        token: result.token,
        isLoading: false,
        isAuthenticated: true,
      });

      logger.info('Login successful', { username: result.username });
    } catch (error) {
      logger.error('Login error', { error });
      throw error;
    }
  }, []);

  const logout = useCallback(async () => {
    logger.debug('Logging out');

    try {
      // Optionally call logout endpoint to invalidate token on server
      // For now, just clear local storage
      localStorage.removeItem('auth_token');
      localStorage.removeItem('auth_user');
      localStorage.removeItem('auth_expires_at');

      setState({
        user: null,
        token: null,
        isLoading: false,
        isAuthenticated: false,
      });

      logger.info('Logout successful');
    } catch (error) {
      logger.error('Logout error', { error });
      throw error;
    }
  }, []);

  const refreshToken = useCallback(async () => {
    const currentToken = localStorage.getItem('auth_token');
    if (!currentToken) {
      logger.debug('No token to refresh');
      return;
    }

    logger.debug('Refreshing token');

    try {
      const response = await fetch('/api/refresh-token', {
        method: 'POST',
        headers: {
          'Authorization': `Bearer ${currentToken}`,
          'Content-Type': 'application/json',
        },
      });

      if (!response.ok) {
        logger.error('Token refresh failed', { status: response.status });
        // Token refresh failed, logout user
        await logout();
        return;
      }

      const result = await response.json();

      if (!result.token) {
        logger.error('Invalid refresh response', { result });
        await logout();
        return;
      }

      // Update token and expiry
      localStorage.setItem('auth_token', result.token);
      localStorage.setItem('auth_expires_at', result.expiresAt);

      setState(prev => ({
        ...prev,
        token: result.token,
      }));

      logger.info('Token refreshed successfully');
    } catch (error) {
      logger.error('Token refresh error', { error });
      await logout();
    }
  }, [logout]);

  const value: AuthContextValue = {
    ...state,
    login,
    logout,
    refreshToken,
    checkAuth,
  };

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth() {
  const context = useContext(AuthContext);
  if (context === undefined) {
    throw new Error('useAuth must be used within an AuthProvider');
  }
  return context;
}

// Higher-order component to protect routes
export function withAuth<P extends object>(
  Component: React.ComponentType<P>
): React.FC<P> {
  return function AuthenticatedComponent(props: P) {
    const { isAuthenticated, isLoading } = useAuth();

    useEffect(() => {
      if (!isLoading && !isAuthenticated) {
        // Redirect to login page
        window.location.href = '/login';
      }
    }, [isAuthenticated, isLoading]);

    if (isLoading) {
      return (
        <div style={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          minHeight: '100vh',
        }}>
          <div>Loading...</div>
        </div>
      );
    }

    if (!isAuthenticated) {
      return null;
    }

    return <Component {...props} />;
  };
}
