import { glob } from 'astro/loaders';
import { z } from 'astro/zod';
import { defineCollection } from 'astro:content';

const blog = defineCollection({
  loader: glob({ pattern: '**/*.md', base: './src/content/blog' }),
  schema: z.object({
    title: z.string().min(1),
    description: z.string().min(1),
    pubDate: z.coerce.date(),
    author: z.string().default('docmatch'),
    readingMinutes: z.number().int().positive().optional(),
    draft: z.boolean().default(false),
  }),
});

export const collections = { blog };
