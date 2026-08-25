import adapter from '@sveltejs/adapter-static';

export default {
  kit: {
    csp: {
      mode: 'hash',
      directives: {
        'default-src': ['self'],
        'base-uri': ['none'],
        'form-action': ['self'],
        'img-src': ['self', 'data:'],
        'style-src': ['self'],
        'script-src': ['self'],
        'connect-src': ['self'],
        'object-src': ['none']
      }
    },
    version: {
      name: 'admin-ui-v1',
      pollInterval: 0
    },
    adapter: adapter({
      pages: '../internal/adminui/assets',
      assets: '../internal/adminui/assets',
      fallback: 'index.html',
      precompress: false,
      strict: true
    })
  }
};
