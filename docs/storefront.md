# Storefronts

Two ways to create a storefront:

1. [Fully headless](#fully-headless)
2. [SSG](#ssg)

## Fully headless

In a fully headless storefront, the frontend is completely decoupled from the backend. The frontend communicates with the backend via APIs, allowing for maximum flexibility in design and 
user experience. This approach is ideal for businesses that want to create a unique and customized storefront.

## SSG

In a Static Site Generated (SSG) storefront, the frontend is built using static site generation techniques. The storefront is generated at build time, and the resulting static files are 
served to users. This approach can offer improved performance and security, as there is no server-side processing required for each request. It is suitable for businesses that want a fast 
and secure storefront with less frequent updates.

Merchants can customize the look and feel of their storefronts from GitStore Admin, which provides a user-friendly interface for managing storefront settings, themes, and content. This 
allows merchants to easily create and maintain their storefronts without needing to directly interact with the underlying code or infrastructure.

In order to avoid complete page builds on every change, we will implement an Incremental Static Regeneration (ISR) strategy. This means that when a product or category is updated, only the 
affected pages will be re-generated, rather than the entire site. This approach allows for faster updates and improved performance, while still providing the benefits of static site 
generation.
